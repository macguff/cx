package cx

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const wakeAgentLabel = "io.github.macguff.cx.wake"

// Restart only a scheduler whose manager confirms it owns the listener. Never
// signal the PID saved in web.json, which could have been reused.
func (a *App) upgradeWakeWeb(address wakeWebAddress, identity string, available func() (wakeWebAddress, string, bool)) error {
	lock, err := a.lockWake("service", false)
	if err != nil {
		return err
	}
	defer unlockWake(lock)
	current, buildID, ok := available()
	if ok && buildID == identity {
		return nil
	}
	if !ok || current.Token != address.Token {
		return errors.New("后台状态已变化，请重新运行 cx wake web")
	}
	if _, _, err := a.wakeInstallation(); err != nil {
		return err
	}
	// A restart would cancel a request. Leave it running and ask the caller to
	// retry after it completes; the ordinary scheduler uses this same lock.
	execution, err := a.lockWake("execution", true)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errors.New("后台正在执行唤醒请求，请在请求完成后重新运行 cx wake web 以自动更新")
	}
	if err != nil {
		return err
	}
	defer unlockWake(execution)
	if runtime.GOOS == "linux" {
		process, configured, err := a.wakeProcessState()
		if err != nil {
			return err
		}
		if configured && process.Running && process.PID == current.PID {
			if _, err := fmt.Fprintln(a.Stdout, "Updating the background scheduler; tasks and history are kept."); err != nil {
				return err
			}
			if err := a.wakeProcessService("uninstall"); err != nil {
				return err
			}
			return a.startWakeProcess()
		}
	}
	if _, err := os.Stat(a.wakeServicePath()); err == nil {
		manager, args := "systemctl", []string{"--user", "show", "cx-wake.service", "--property=MainPID", "--value"}
		if runtime.GOOS == "darwin" {
			manager, args = "launchctl", []string{"print", "gui/" + strconv.Itoa(os.Getuid()) + "/" + wakeAgentLabel}
		}
		binary, err := a.LookPath(manager)
		if err == nil {
			var output bytes.Buffer
			if err := a.Runner.Run(binary, args, os.Environ(), nil, &output, io.Discard); err == nil {
				pidText := strings.TrimSpace(output.String())
				if runtime.GOOS == "darwin" {
					pidText = ""
					for _, line := range strings.Split(output.String(), "\n") {
						if key, value, ok := strings.Cut(strings.TrimSpace(line), " = "); ok && key == "pid" {
							pidText = value
							break
						}
					}
				}
				pid, _ := strconv.Atoi(pidText)
				if pid > 0 && pid == current.PID {
					if _, err := fmt.Fprintln(a.Stdout, "Updating the background scheduler; tasks and history are kept."); err != nil {
						return err
					}
					return a.wakeManagedService("install")
				}
			}
		}
	}
	return errors.New("现有 Web 后台不是可识别的托管服务；请在运行它的终端停止后，再运行 cx wake web")
}

func (a *App) wakeServicePath() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "LaunchAgents", wakeAgentLabel+".plist")
	}
	return filepath.Join(filepath.Dir(a.Paths.ConfigDir), "systemd", "user", "cx-wake.service")
}

func (a *App) wakeServiceHint() string {
	if runtime.GOOS == "linux" {
		process, configured, err := a.wakeProcessState()
		if err != nil {
			return "unable to inspect process backend; use 'cx wake status'"
		}
		if configured {
			if process.Running {
				return fmt.Sprintf("process running (PID %d); restart with 'cx wake install' after reboot", process.PID)
			}
			return "process stopped; use 'cx wake install' to restart it"
		}
	}
	if _, err := os.Stat(a.wakeServicePath()); err == nil {
		return "configured; check 'cx wake status' to verify it is active"
	}
	return "not installed; use 'cx wake install' or keep 'cx wake serve' running"
}

func (a *App) wakeService(args []string) error {
	if runtime.GOOS == "linux" && len(args) == 2 && args[0] == "_worker" {
		return a.runWakeWorker(args[1])
	}
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status") {
		return wakeUsageError("use 'cx wake install|uninstall|status'")
	}
	lock, err := a.lockWake("service", false)
	if err != nil {
		return err
	}
	defer unlockWake(lock)
	if runtime.GOOS == "linux" {
		_, configured, err := a.wakeProcessState()
		if err != nil {
			return err
		}
		if configured {
			return a.wakeProcessService(args[0])
		}
		if args[0] == "install" {
			if err := a.probeWakeSystemd(); err != nil {
				if _, err := fmt.Fprintln(a.Stdout, "User systemd is unavailable; using the cx background process backend."); err != nil {
					return err
				}
				return a.wakeProcessService("install")
			}
		} else if _, err := os.Stat(a.wakeServicePath()); err == nil {
			if err := a.probeWakeSystemd(); err != nil {
				return fmt.Errorf("systemd unit files exist, but the user manager is unavailable: %w; run 'cx wake install' for the process backend, or use 'cx wake serve'", err)
			}
		}
	}
	return a.wakeManagedService(args[0])
}

func (a *App) probeWakeSystemd() error {
	binary, err := a.LookPath("systemctl")
	if err != nil {
		return errors.New("systemctl was not found")
	}
	var detail bytes.Buffer
	if err := a.Runner.Run(binary, []string{"--user", "show", "--property=Version"}, os.Environ(), nil, io.Discard, &detail); err != nil {
		message := strings.TrimSpace(detail.String())
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

func (a *App) wakeManagedService(action string) error {
	path := a.wakeServicePath()
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
		manager = "launchctl"
	}
	binary, err := a.LookPath(manager)
	if err != nil {
		return fmt.Errorf("%s was not found; use 'cx wake serve' or run 'cx wake tick' from your scheduler", manager)
	}
	run := func(argv ...string) error {
		if err := a.Runner.Run(binary, argv, os.Environ(), nil, a.Stdout, a.Stderr); err != nil {
			return fmt.Errorf("%s %s failed: %w", manager, strings.Join(argv, " "), err)
		}
		return nil
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if action == "status" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			_, err := fmt.Fprintln(a.Stdout, "Background scheduler is not installed. Use 'cx wake install' or 'cx wake serve'.")
			return err
		} else if err != nil {
			return err
		}
		if runtime.GOOS == "darwin" {
			return run("print", domain+"/"+wakeAgentLabel)
		}
		return run("--user", "status", "cx-wake.service", "--no-pager")
	}
	if action == "uninstall" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			_, err := fmt.Fprintln(a.Stdout, "Background scheduler is not installed.")
			return err
		} else if err != nil {
			return err
		}
		if runtime.GOOS == "darwin" {
			if a.wakeAgentLoaded(binary, domain) {
				if err := run("bootout", domain, path); err != nil {
					return err
				}
			}
		} else {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "cx-wake.timer")); err == nil {
				if err := run("--user", "disable", "--now", "cx-wake.timer"); err != nil {
					return err
				}
			}
			if err := run("--user", "disable", "--now", "cx-wake.service"); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if runtime.GOOS == "linux" {
			if err := os.Remove(filepath.Join(filepath.Dir(path), "cx-wake.timer")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := run("--user", "daemon-reload"); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(a.Stdout, "Background scheduler removed. Tasks and history are kept.")
		return err
	}
	executable, env, err := a.wakeInstallation()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(a.wakeDir(), 0o700); err != nil {
			return err
		}
		logPath := filepath.Join(a.wakeDir(), "scheduler.log")
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := log.Close(); err != nil {
			return err
		}
		if a.wakeAgentLoaded(binary, domain) {
			if err := run("bootout", domain, path); err != nil {
				return err
			}
		}
		if err := writeFileAtomic(path, []byte(wakeLaunchAgent(executable, env, logPath)), 0o600); err != nil {
			return err
		}
		if err := run("bootstrap", domain, path); err != nil {
			return err
		}
	} else {
		legacy := filepath.Join(filepath.Dir(path), "cx-wake.timer")
		if _, err := os.Stat(legacy); err == nil {
			if err := run("--user", "disable", "--now", "cx-wake.timer"); err != nil {
				return err
			}
			if err := os.Remove(legacy); err != nil {
				return err
			}
		}
		service := wakeSystemdUnit(executable, env)
		if err := writeFileAtomic(path, []byte(service), 0o600); err != nil {
			return err
		}
		if err := run("--user", "daemon-reload"); err != nil {
			return err
		}
		if err := run("--user", "enable", "cx-wake.service"); err != nil {
			return err
		}
		if err := run("--user", "restart", "cx-wake.service"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(a.Stdout, "Background scheduler and Web UI enabled (checks every 15 seconds while awake).\nRun 'cx wake web' for the access link. Reinstall after moving cx or changing your Codex installation/PATH.")
	return err
}

func (a *App) wakeInstallation() (string, map[string]string, error) {
	if _, err := a.LookPath("codex"); err != nil {
		return "", nil, errors.New("codex executable was not found in PATH")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", nil, err
	}
	if strings.HasPrefix(executable, os.TempDir()+string(os.PathSeparator)) && strings.Contains(executable, "go-build") {
		return "", nil, errors.New("build and install cx first, then run 'cx wake install' from that stable binary")
	}
	env := map[string]string{"PATH": os.Getenv("PATH")}
	for key, value := range map[string]string{"XDG_CONFIG_HOME": filepath.Dir(a.Paths.ConfigDir), "XDG_DATA_HOME": filepath.Dir(a.Paths.DataDir)} {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", nil, err
		}
		env[key] = absolute
	}
	if tz := os.Getenv("TZ"); tz != "" {
		env["TZ"] = tz
	}
	if bind := os.Getenv("CX_WAKE_WEB_ADDR"); bind != "" {
		env["CX_WAKE_WEB_ADDR"] = bind
	}
	if strings.ContainsAny(executable, "\r\n\x00") {
		return "", nil, errors.New("cx executable path contains unsupported characters")
	}
	for _, value := range env {
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", nil, errors.New("scheduler environment contains unsupported characters")
		}
	}
	return executable, env, nil
}

func (a *App) wakeAgentLoaded(binary, domain string) bool {
	return a.Runner.Run(binary, []string{"print", domain + "/" + wakeAgentLabel}, os.Environ(), nil, io.Discard, io.Discard) == nil
}

func wakeSystemdUnit(executable string, env map[string]string) string {
	quote := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + strings.ReplaceAll(s, "%", "%%") + `"`
	}
	service := "[Unit]\nDescription=cx Codex wake-up scheduler and Web UI\n\n[Service]\nType=simple\n"
	// systemd forbids '$' in the executable token. env passes the quoted path as
	// an ordinary argument and then execs cx, without involving a shell.
	service += "ExecStart=/usr/bin/env -- " + quote(strings.ReplaceAll(executable, "$", "$$")) + " wake serve\n"
	keys := sortedWakeEnvKeys(env)
	for _, key := range keys {
		service += "Environment=" + quote(key+"="+env[key]) + "\n"
	}
	return service + "UMask=0077\nRestart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=default.target\n"
}

func wakeLaunchAgent(executable string, env map[string]string, logPath string) string {
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + wakeAgentLabel + `</string>
<key>ProgramArguments</key><array><string>` + escape(executable) + `</string><string>wake</string><string>serve</string></array>
<key>EnvironmentVariables</key><dict>`
	for _, key := range sortedWakeEnvKeys(env) {
		plist += "<key>" + key + "</key><string>" + escape(env[key]) + "</string>"
	}
	return plist + `</dict>
<key>KeepAlive</key><true/>
<key>RunAtLoad</key><true/>
<key>StandardOutPath</key><string>` + escape(logPath) + `</string>
<key>StandardErrorPath</key><string>` + escape(logPath) + `</string>
</dict></plist>
`
}

func sortedWakeEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
