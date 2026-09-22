package cx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const profileConfig = `cli_auth_credentials_store = "file"
`

type Config struct {
	Version int    `json:"version"`
	Current string `json:"current,omitempty"`
}

type Paths struct {
	ConfigDir  string
	ConfigFile string
	DataDir    string
	Profiles   string
}

type CommandRunner interface {
	Run(path string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error
	Exec(path string, args []string, env []string) error
}

type App struct {
	Paths    Paths
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Runner   CommandRunner
	LookPath func(string) (string, error)
}

type ExitError struct {
	Code int
	Err  error
}

func (e ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("command exited with status %d", e.Code)
	}
	return e.Err.Error()
}

func New() (*App, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	dataBase := os.Getenv("XDG_DATA_HOME")
	if dataBase == "" {
		dataBase = filepath.Join(home, ".local", "share")
	}

	return NewWithPaths(Paths{
		ConfigDir:  filepath.Join(configBase, "cx"),
		ConfigFile: filepath.Join(configBase, "cx", "config.json"),
		DataDir:    filepath.Join(dataBase, "cx"),
		Profiles:   filepath.Join(dataBase, "cx", "profiles"),
	}), nil
}

func NewWithPaths(paths Paths) *App {
	return &App{
		Paths:    paths,
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Runner:   systemRunner{},
		LookPath: exec.LookPath,
	}
}

func (a *App) Run(args []string) error {
	if len(args) == 0 {
		return a.runCurrent(nil)
	}

	switch args[0] {
	case "help", "--help", "-h":
		return a.printHelp()
	case "--version", "-V":
		_, err := fmt.Fprintf(a.Stdout, "cx %s\n", BuildVersion)
		return err
	case "login":
		return a.login(args[1:])
	case "use":
		return a.use(args[1:])
	case "switch":
		return a.switchProfile(args[1:])
	case "run":
		return a.runProfile(args[1:])
	case "resume":
		return a.runCurrent(args)
	case "list":
		return a.list(args[1:])
	case "current":
		return a.current(args[1:])
	case "path":
		return a.path(args[1:])
	case "logout":
		return a.logout(args[1:])
	case "import":
		return a.importProfile(args[1:])
	case "status":
		return a.status(args[1:])
	case "usage":
		return a.usage(args[1:])
	case "tui":
		return a.tui(args[1:])
	case "--":
		return a.runCurrent(args[1:])
	default:
		return ExitError{Code: 2, Err: fmt.Errorf("unknown command %q; run 'cx help' for usage", args[0])}
	}
}

func (a *App) printHelp() error {
	_, err := fmt.Fprint(a.Stdout, `cx - run Codex with isolated account profiles

Quick start:
  1. cx login personal           Create a profile and sign in through Codex
  2. cx tui                      Choose an account and start Codex

Usage:
  cx                              Start Codex with the current profile
  cx tui                          Interactively view usage, choose an account,
                                  and start Codex
  cx login <name> [login-args...] Create or sign in to a profile using the
                                  official 'codex login' flow
  cx use <name>                   Make a profile the default for future 'cx' runs
  cx switch                       Switch profiles; automatic when exactly two exist
  cx run <name> [codex-args...]   Start Codex with one profile without changing
                                  the default profile
  cx resume [resume-args...]      Resume a session with the current profile
  cx -- <codex-args...>           Pass arguments to Codex using the current profile
  cx list                         List profiles and show the current profile
  cx current                      Print the current profile name
  cx path <name>                  Print a profile's CODEX_HOME directory
  cx logout <name>                Sign out of one profile through 'codex logout'
  cx import <name>                Copy local usage logs from ~/.codex into a profile
  cx status                       Show local token usage for every profile
  cx usage                        Show local token usage grouped by model
  cx --version                    Print the cx version

Examples:
  cx login personal              Create or sign in to the "personal" profile
  cx login work --device-auth    Sign in to "work" with Codex device authentication
  cx tui                         Compare local usage, select a profile, and launch Codex
  cx use work                    Set "work" as the default profile
  cx switch                      Switch to the other profile or choose from a list
  cx                             Launch Codex with the default profile
  cx run personal                Launch Codex with "personal" for this run only
  cx run personal exec "review this repository"
                                  Run a non-interactive Codex task as "personal"
  cx resume                       Choose a saved session with the default profile
  cx resume --last                Resume the newest session without prompting
  cx run work resume              Choose a saved session as the "work" profile
  cx import personal              Import usage history from ~/.codex
  cx status                       Show each profile's 5-hour, 7-day, and all-time usage
  cx usage                        Compare each model's local token usage
`)
	return err
}

func (a *App) login(args []string) error {
	if len(args) == 0 {
		return ExitError{Code: 2, Err: errors.New("login requires a profile name")}
	}
	name := args[0]
	profile, err := a.ensureProfile(name)
	if err != nil {
		return err
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return errors.New("codex executable was not found in PATH")
	}
	commandArgs := append([]string{"login"}, args[1:]...)
	if err := a.Runner.Run(codex, commandArgs, withCodexHome(os.Environ(), profile), a.Stdin, a.Stdout, a.Stderr); err != nil {
		return commandError("codex login", err)
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if cfg.Current == "" {
		cfg.Current = name
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.Stdout, "Current profile: %s\n", name)
		return err
	}
	return nil
}

func (a *App) use(args []string) error {
	if len(args) != 1 {
		return ExitError{Code: 2, Err: errors.New("use requires exactly one profile name")}
	}
	name := args[0]
	profile, err := a.profilePath(name)
	if err != nil {
		return err
	}
	if info, err := os.Stat(profile); err != nil || !info.IsDir() {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("profile %q does not exist; create it with 'cx login %s'", name, name)
		}
		if err != nil {
			return fmt.Errorf("inspect profile %q: %w", name, err)
		}
		return fmt.Errorf("profile path for %q is not a directory", name)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	cfg.Current = name
	if err := a.saveConfig(cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "Current profile: %s\n", name)
	return err
}

func (a *App) runProfile(args []string) error {
	if len(args) == 0 {
		return ExitError{Code: 2, Err: errors.New("run requires a profile name")}
	}
	return a.execCodex(args[0], args[1:])
}

func (a *App) runCurrent(args []string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if cfg.Current == "" {
		return errors.New("no current profile; create one with 'cx login <name>' or select one with 'cx use <name>'")
	}
	return a.execCodex(cfg.Current, args)
}

func (a *App) execCodex(name string, args []string) error {
	profile, err := a.existingProfile(name)
	if err != nil {
		return err
	}
	if err := ensureCredentialStore(profile); err != nil {
		return err
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return errors.New("codex executable was not found in PATH")
	}
	return a.Runner.Exec(codex, append([]string{"codex"}, args...), withCodexHome(os.Environ(), profile))
}

func (a *App) logout(args []string) error {
	if len(args) != 1 {
		return ExitError{Code: 2, Err: errors.New("logout requires exactly one profile name")}
	}
	profile, err := a.existingProfile(args[0])
	if err != nil {
		return err
	}
	if err := ensureCredentialStore(profile); err != nil {
		return err
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return errors.New("codex executable was not found in PATH")
	}
	if err := a.Runner.Run(codex, []string{"logout"}, withCodexHome(os.Environ(), profile), a.Stdin, a.Stdout, a.Stderr); err != nil {
		return commandError("codex logout", err)
	}
	return nil
}

func (a *App) list(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("list does not accept arguments")}
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(a.Paths.Profiles)
	if errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintln(a.Stdout, "No profiles. Create one with 'cx login <name>'.")
		return err
	}
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && validProfileName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		_, err = fmt.Fprintln(a.Stdout, "No profiles. Create one with 'cx login <name>'.")
		return err
	}
	for _, name := range names {
		marker := " "
		if name == cfg.Current {
			marker = "*"
		}
		status := "not logged in"
		if info, statErr := os.Stat(filepath.Join(a.Paths.Profiles, name, "auth.json")); statErr == nil && !info.IsDir() {
			status = "logged in"
		}
		if _, err := fmt.Fprintf(a.Stdout, "%s %-20s %s\n", marker, name, status); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) current(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("current does not accept arguments")}
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if cfg.Current == "" {
		return errors.New("no current profile")
	}
	_, err = fmt.Fprintln(a.Stdout, cfg.Current)
	return err
}

func (a *App) path(args []string) error {
	if len(args) != 1 {
		return ExitError{Code: 2, Err: errors.New("path requires exactly one profile name")}
	}
	profile, err := a.existingProfile(args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, profile)
	return err
}

func (a *App) ensureProfile(name string) (string, error) {
	profile, err := a.profilePath(name)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{a.Paths.DataDir, a.Paths.Profiles, profile} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create profile storage: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("secure profile storage: %w", err)
		}
	}
	if err := ensureCredentialStore(profile); err != nil {
		return "", err
	}
	return profile, nil
}

func (a *App) existingProfile(name string) (string, error) {
	profile, err := a.profilePath(name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(profile)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("profile %q does not exist; create it with 'cx login %s'", name, name)
	}
	if err != nil {
		return "", fmt.Errorf("inspect profile %q: %w", name, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("profile path for %q is not a directory", name)
	}
	return profile, nil
}

func (a *App) profilePath(name string) (string, error) {
	if !validProfileName(name) {
		return "", fmt.Errorf("invalid profile name %q; use letters, numbers, '.', '_' or '-'", name)
	}
	return filepath.Join(a.Paths.Profiles, name), nil
}

func validProfileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func ensureCredentialStore(profile string) error {
	path := filepath.Join(profile, "config.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte(profileConfig), 0o600); err != nil {
			return fmt.Errorf("create profile config: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read profile config: %w", err)
	}

	found, value := rootCredentialStore(data)
	if found {
		if value != "file" {
			return fmt.Errorf("%s must set cli_auth_credentials_store to \"file\", got %q", path, value)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure profile config: %w", err)
		}
		return nil
	}

	updated := append([]byte(profileConfig), data...)
	if err := writeFileAtomic(path, updated, 0o600); err != nil {
		return fmt.Errorf("update profile config: %w", err)
	}
	return nil
}

func rootCredentialStore(data []byte) (bool, string) {
	inRoot := true
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inRoot = false
			continue
		}
		if !inRoot {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "cli_auth_credentials_store" {
			continue
		}
		value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
		value = strings.Trim(value, "\"'")
		return true, value
	}
	return false, ""
}

func (a *App) loadConfig() (Config, error) {
	data, err := os.ReadFile(a.Paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read cx config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse cx config: %w", err)
	}
	if cfg.Version != 1 {
		return Config{}, fmt.Errorf("unsupported cx config version %d", cfg.Version)
	}
	if cfg.Current != "" && !validProfileName(cfg.Current) {
		return Config{}, errors.New("cx config contains an invalid current profile")
	}
	return cfg, nil
}

func (a *App) saveConfig(cfg Config) error {
	cfg.Version = 1
	if err := os.MkdirAll(a.Paths.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create cx config directory: %w", err)
	}
	if err := os.Chmod(a.Paths.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("secure cx config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cx config: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(a.Paths.ConfigFile, data, 0o600); err != nil {
		return fmt.Errorf("write cx config: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".cx-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func withCodexHome(env []string, home string) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, "CODEX_HOME=") {
			result = append(result, item)
		}
	}
	return append(result, "CODEX_HOME="+home)
}

func commandError(name string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExitError{Code: exitErr.ExitCode()}
	}
	return fmt.Errorf("run %s: %w", name, err)
}
