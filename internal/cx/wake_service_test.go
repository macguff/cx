package cx

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestWakeSystemdProbeChecksUserManager(t *testing.T) {
	a, runner, _ := newTestApp(t)
	runner.runErr = errors.New("exit status 1")
	runner.runStderr = "Failed to connect to bus: No such file or directory\n"
	if err := a.probeWakeSystemd(); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("missing user bus was not detected: %v", err)
	}
	if len(runner.run) != 1 || strings.Join(runner.run[0].args, "|") != "--user|show|--property=Version" {
		t.Fatalf("unexpected manager probe: %#v", runner.run)
	}
	if _, err := os.Stat(a.wakeServicePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe wrote a timer file before finding a working manager")
	}
	runner.runErr, runner.runStderr = nil, ""
	if err := a.probeWakeSystemd(); err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := a.probeWakeSystemd(); err == nil {
		t.Fatal("missing systemctl was accepted")
	}
}

func TestWakeServiceDefinitionsPreservePathsWithoutCredentials(t *testing.T) {
	env := map[string]string{
		"PATH":            "/Users/a/Node & Tools/bin:/usr/bin",
		"XDG_CONFIG_HOME": "/Users/a/Config <work>",
		"XDG_DATA_HOME":   "/Users/a/Data 100%",
		"TZ":              "Asia/Shanghai",
	}
	executable := "/Users/a/My \"Tools\"/$bin/cx"
	service := wakeSystemdUnit(executable, env)
	if !strings.Contains(service, `ExecStart=/usr/bin/env -- "/Users/a/My \"Tools\"/$$bin/cx" wake serve`) ||
		!strings.Contains(service, `Environment="XDG_DATA_HOME=/Users/a/Data 100%%"`) ||
		!strings.Contains(service, "Type=simple") || !strings.Contains(service, "Restart=on-failure") || !strings.Contains(service, "WantedBy=default.target") {
		t.Fatalf("systemd definition does not preserve paths or continuous scheduling: %s", service)
	}
	plist := wakeLaunchAgent(executable, env, "/Users/a/Data & Logs/wake.log")
	decoder := xml.NewDecoder(strings.NewReader(plist))
	var values []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("invalid LaunchAgent XML:", err)
		}
		if start, ok := token.(xml.StartElement); ok && (start.Name.Local == "string" || start.Name.Local == "integer") {
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
	}
	for _, want := range []string{executable, "wake", "serve", env["PATH"], env["XDG_CONFIG_HOME"], env["XDG_DATA_HOME"], "/Users/a/Data & Logs/wake.log"} {
		found := false
		for _, got := range values {
			found = found || got == want
		}
		if !found {
			t.Fatalf("LaunchAgent lost value %q: %#v", want, values)
		}
	}
	for _, data := range []string{service, plist} {
		if strings.Contains(data, "API_KEY") || strings.Contains(data, "auth.json") || strings.Contains(data, "CODEX_HOME") {
			t.Fatal("service contains credentials or a shared profile override")
		}
	}
}
