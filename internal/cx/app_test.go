package cx

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordedCall struct {
	path string
	args []string
	env  []string
}

type fakeRunner struct {
	run       []recordedCall
	exec      []recordedCall
	runOutput string
	runStderr string
	runErr    error
}

func (r *fakeRunner) Run(path string, args []string, env []string, _ io.Reader, stdout, stderr io.Writer) error {
	r.run = append(r.run, recordedCall{path: path, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	if r.runOutput != "" {
		_, _ = io.WriteString(stdout, r.runOutput)
	}
	if r.runStderr != "" {
		_, _ = io.WriteString(stderr, r.runStderr)
	}
	return r.runErr
}

func (r *fakeRunner) Exec(path string, args []string, env []string) error {
	r.exec = append(r.exec, recordedCall{path: path, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	return nil
}

func newTestApp(t *testing.T) (*App, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	out := new(bytes.Buffer)
	runner := new(fakeRunner)
	app := NewWithPaths(Paths{
		ConfigDir:  filepath.Join(root, "config"),
		ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDir:    filepath.Join(root, "data"),
		Profiles:   filepath.Join(root, "data", "profiles"),
	})
	app.Stdout = out
	app.Stderr = new(bytes.Buffer)
	app.Stdin = strings.NewReader("")
	app.Runner = runner
	app.LookPath = func(string) (string, error) { return "/usr/bin/codex", nil }
	return app, runner, out
}

func TestVersionUsesBuildVersion(t *testing.T) {
	app, _, out := newTestApp(t)
	previous := BuildVersion
	BuildVersion = "1.0.0-test"
	t.Cleanup(func() { BuildVersion = previous })
	if err := app.Run([]string{"--version"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "cx 1.0.0-test\n" {
		t.Fatalf("unexpected version output: %q", got)
	}
}

func TestLoginCreatesIsolatedProfileAndSelectsFirstProfile(t *testing.T) {
	app, runner, out := newTestApp(t)
	if err := app.Run([]string{"login", "work", "--device-auth"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.run) != 1 {
		t.Fatalf("expected one login call, got %d", len(runner.run))
	}
	call := runner.run[0]
	if strings.Join(call.args, "|") != "login|--device-auth" {
		t.Fatalf("unexpected login args: %#v", call.args)
	}
	profile := filepath.Join(app.Paths.Profiles, "work")
	if got := envValue(call.env, "CODEX_HOME"); got != profile {
		t.Fatalf("CODEX_HOME = %q, want %q", got, profile)
	}
	config, err := os.ReadFile(filepath.Join(profile, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(config) != profileConfig {
		t.Fatalf("unexpected profile config: %q", config)
	}
	if got := out.String(); got != "Current profile: work\n" {
		t.Fatalf("unexpected output: %q", got)
	}
	cfg, err := app.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "work" {
		t.Fatalf("current profile = %q", cfg.Current)
	}
}

func TestUseAndRunProfile(t *testing.T) {
	app, runner, out := newTestApp(t)
	profile, err := app.ensureProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"use", "work"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Current profile: work\n" {
		t.Fatalf("unexpected output: %q", got)
	}

	if err := app.Run([]string{"--", "exec", "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"resume", "--last"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.exec) != 2 {
		t.Fatalf("expected two exec calls, got %d", len(runner.exec))
	}
	call := runner.exec[0]
	if strings.Join(call.args, "|") != "codex|exec|hello" {
		t.Fatalf("unexpected args: %#v", call.args)
	}
	if got := envValue(call.env, "CODEX_HOME"); got != profile {
		t.Fatalf("CODEX_HOME = %q, want %q", got, profile)
	}
	if got := strings.Join(runner.exec[1].args, "|"); got != "codex|resume|--last" {
		t.Fatalf("unexpected resume args: %q", got)
	}
}

func TestSwitchChangesToOtherProfileWhenExactlyTwoExist(t *testing.T) {
	app, _, out := newTestApp(t)
	for _, name := range []string{"personal", "work"} {
		if _, err := app.ensureProfile(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Run([]string{"use", "work"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run([]string{"switch"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Current profile: personal\n" {
		t.Fatalf("unexpected switch output: %q", got)
	}
	cfg, err := app.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "personal" {
		t.Fatalf("current profile = %q, want personal", cfg.Current)
	}
}

func TestSwitchPromptsWhenMoreThanTwoProfilesExist(t *testing.T) {
	app, _, out := newTestApp(t)
	for _, name := range []string{"personal", "staging", "work"} {
		if _, err := app.ensureProfile(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Run([]string{"use", "personal"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	app.Stdin = strings.NewReader("3\n")
	if err := app.Run([]string{"switch"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "* 1. personal") || !strings.Contains(out.String(), "3. work") {
		t.Fatalf("unexpected switch menu: %s", out.String())
	}
	cfg, err := app.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "work" {
		t.Fatalf("current profile = %q, want work", cfg.Current)
	}
}

func TestProfilesRemainIsolated(t *testing.T) {
	app, runner, _ := newTestApp(t)
	personal, err := app.ensureProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	work, err := app.ensureProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"run", "personal"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"run", "work", "exec", "status"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.exec) != 2 {
		t.Fatalf("expected two exec calls, got %d", len(runner.exec))
	}
	if got := envValue(runner.exec[0].env, "CODEX_HOME"); got != personal {
		t.Fatalf("personal CODEX_HOME = %q", got)
	}
	if got := envValue(runner.exec[1].env, "CODEX_HOME"); got != work {
		t.Fatalf("work CODEX_HOME = %q", got)
	}
}

func TestEnsureCredentialStore(t *testing.T) {
	profile := t.TempDir()
	config := filepath.Join(profile, "config.toml")
	if err := os.WriteFile(config, []byte("model = \"gpt-test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialStore(profile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), profileConfig) {
		t.Fatalf("credential setting missing from %q", data)
	}

	if err := os.WriteFile(config, []byte("cli_auth_credentials_store = \"keyring\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialStore(profile); err == nil {
		t.Fatal("expected non-file credential store to be rejected")
	}
}

func TestEnsureCredentialStoreWritesTopLevelBeforeTables(t *testing.T) {
	profile := t.TempDir()
	config := filepath.Join(profile, "config.toml")
	if err := os.WriteFile(config, []byte("[history]\npersistence = \"none\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialStore(profile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), profileConfig+"[history]") {
		t.Fatalf("credential setting is not at the TOML root: %q", data)
	}
	info, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func TestListDoesNotReadCredentials(t *testing.T) {
	app, _, out := newTestApp(t)
	profile, err := app.ensureProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "auth.json"), []byte("not-json-and-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "work") || !strings.Contains(got, "logged in") {
		t.Fatalf("unexpected list output: %q", got)
	}
}

func TestRejectsUnsafeProfileNames(t *testing.T) {
	app, _, _ := newTestApp(t)
	for _, name := range []string{"", ".", "..", "../work", "work/home", "work account"} {
		if _, err := app.profilePath(name); err == nil {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestTUISelectsProfileAndLaunchesCodex(t *testing.T) {
	app, runner, out := newTestApp(t)
	for _, name := range []string{"personal", "work"} {
		profile, err := app.ensureProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profile, "auth.json"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		sessions := filepath.Join(profile, "sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
			usageEvent("2026-09-21T01:00:00Z", usageFields(100, 80, 10, 4), usageFields(100, 80, 10, 4)),
		})
	}
	app.Stdin = strings.NewReader("2\n")

	if err := app.Run([]string{"tui"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.exec) != 1 {
		t.Fatalf("expected one launch call, got %d", len(runner.exec))
	}
	if got := envValue(runner.exec[0].env, "CODEX_HOME"); got != filepath.Join(app.Paths.Profiles, "work") {
		t.Fatalf("CODEX_HOME = %q", got)
	}
	cfg, err := app.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "work" {
		t.Fatalf("current profile = %q, want work", cfg.Current)
	}
	for _, expected := range []string{"personal", "work", "110", "Combined: 220 tokens"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("TUI output does not contain %q: %s", expected, out.String())
		}
	}
}

func TestTUICreatesFirstProfileWithOfficialLogin(t *testing.T) {
	app, runner, out := newTestApp(t)
	app.Stdin = strings.NewReader("n\nwork\nq\n")
	if err := app.Run([]string{"tui"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.run) != 1 || strings.Join(runner.run[0].args, "|") != "login" {
		t.Fatalf("unexpected login calls: %#v", runner.run)
	}
	if _, err := os.Stat(filepath.Join(app.Paths.Profiles, "work", "config.toml")); err != nil {
		t.Fatalf("profile was not created: %v", err)
	}
	if !strings.Contains(out.String(), "No profiles yet") || !strings.Contains(out.String(), "work") {
		t.Fatalf("unexpected TUI output: %s", out.String())
	}
}

func TestTUIChangesUsageRangeWithoutNetworkRefresh(t *testing.T) {
	app, _, out := newTestApp(t)
	if _, err := app.ensureProfile("work"); err != nil {
		t.Fatal(err)
	}
	app.Stdin = strings.NewReader("r\nq\n")
	if err := app.Run([]string{"tui"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Last 30 days") || !strings.Contains(out.String(), "All time") {
		t.Fatalf("range did not change: %s", out.String())
	}
}

func TestStatusShowsLocalUsageForEveryProfile(t *testing.T) {
	app, runner, out := newTestApp(t)
	runner.runStderr = "WARNING: ignored test warning\nLogged in using ChatGPT\n"
	now := time.Now().UTC()
	for _, profileName := range []string{"personal", "work"} {
		profile, err := app.ensureProfile(profileName)
		if err != nil {
			t.Fatal(err)
		}
		sessions := filepath.Join(profile, "sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
			usageEvent(now.Add(-time.Hour).Format(time.RFC3339Nano), usageFields(100, 0, 10, 0), usageFields(100, 0, 10, 0)),
			usageEvent(now.Add(-6*time.Hour).Format(time.RFC3339Nano), usageFields(200, 0, 20, 0), usageFields(100, 0, 10, 0)),
		})
	}
	if err := os.WriteFile(filepath.Join(app.Paths.Profiles, "work", "auth.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"use", "work"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Profile: personal", "Profile: work (current)", "Auth: not signed in", "Auth: Logged in using ChatGPT", "Limits: run 'cx run work', then enter '/status'", "Last 5 hours", "Last 7 days", "All time", "110 tokens", "220 tokens"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("status output does not contain %q: %s", expected, out.String())
		}
	}
	if len(runner.run) != 1 || strings.Join(runner.run[0].args, "|") != "login|status" {
		t.Fatalf("unexpected login status calls: %#v", runner.run)
	}
	if got := envValue(runner.run[0].env, "CODEX_HOME"); got != filepath.Join(app.Paths.Profiles, "work") {
		t.Fatalf("login status CODEX_HOME = %q", got)
	}
}

func TestUsageShowsLocalUsageGroupedByModel(t *testing.T) {
	app, _, out := newTestApp(t)
	profile, err := app.ensureProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
		usageContext("gpt-5.6-sol"),
		usageEvent(now.Add(-time.Hour).Format(time.RFC3339Nano), usageFields(1_000_000, 0, 100_000, 0), usageFields(1_000_000, 0, 100_000, 0)),
		usageContext("gpt-5.6-luna"),
		usageEvent(now.Add(-30*time.Minute).Format(time.RFC3339Nano), usageFields(1_100_000, 0, 110_000, 0), usageFields(100_000, 0, 10_000, 0)),
	})
	if err := app.Run([]string{"use", "work"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run([]string{"usage"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Profile: work (current)", "gpt-5.6-sol", "gpt-5.6-luna", "1.1M", "110K", "MONTHLY TOTALS", now.Format("2006-01"), "Trend:"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("usage output does not contain %q: %s", expected, out.String())
		}
	}
}

func TestImportCopiesOnlyUsageLogsAndSkipsExistingFiles(t *testing.T) {
	app, _, out := newTestApp(t)
	profile, err := app.ensureProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	sourceSessions := filepath.Join(source, "sessions", "2026", "09", "21")
	if err := os.MkdirAll(sourceSessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUsageEvents(t, filepath.Join(sourceSessions, "rollout-old.jsonl"), []any{
		usageEvent("2026-09-21T01:00:00Z", usageFields(100, 0, 10, 0), usageFields(100, 0, 10, 0)),
	})
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := importUsageLogs(source, profile)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Copied != 1 || summary.Skipped != 0 {
		t.Fatalf("first import summary: %+v", summary)
	}
	if _, err := os.Stat(filepath.Join(profile, "sessions", "2026", "09", "21", "rollout-old.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auth.json was copied: %v", err)
	}
	second, err := importUsageLogs(source, profile)
	if err != nil {
		t.Fatal(err)
	}
	if second.Copied != 0 || second.Skipped != 1 {
		t.Fatalf("second import summary: %+v", second)
	}
	_ = out
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
