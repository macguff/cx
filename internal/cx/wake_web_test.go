package cx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWakeWebServerLifecycleAndAccessLink(t *testing.T) {
	t.Setenv("CX_WAKE_WEB_ADDR", "127.0.0.1:0")
	a, _, _ := newWakeTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, _, err := a.startWakeWeb(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	address, err := a.readWakeWeb()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(address.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("live API does not require the access token")
	}
	a.Stdout = new(bytes.Buffer)
	if err := a.wakeWebCommand(nil); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(strings.TrimSpace(a.Stdout.(*bytes.Buffer).String()))
	if err != nil || link.Fragment != "token="+address.Token {
		t.Fatalf("invalid access link: %s, %v", link, err)
	}
	request, _ := http.NewRequest("GET", address.URL+"/api/state", nil)
	request.Header.Set("Authorization", "Bearer "+address.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(data, []byte(`"name":"work"`)) {
		t.Fatalf("live API is not functional: %s", data)
	}
}

func wakeWebRequest(t *testing.T, web *wakeHTTP, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+web.host+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+web.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+web.host)
	response := httptest.NewRecorder()
	web.ServeHTTP(response, req)
	return response
}

func TestWakeWebRejectsCrossOriginAndUnauthenticatedRequests(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	web := &wakeHTTP{app: a, token: strings.Repeat("a", 64), host: "127.0.0.1:7331"}
	for _, tt := range []struct {
		host, origin, token, site string
		want                      int
	}{
		{host: web.host, want: 401},
		{host: "evil.example", token: web.token, want: 403},
		{host: web.host, token: web.token, origin: "https://evil.example", want: 403},
		{host: web.host, token: web.token, site: "cross-site", want: 403},
		{host: web.host, token: web.token, origin: "http://" + web.host, want: 200},
	} {
		req := httptest.NewRequest("GET", "http://"+tt.host+"/api/state", nil)
		if tt.token != "" {
			req.Header.Set("Authorization", "Bearer "+tt.token)
		}
		req.Header.Set("Origin", tt.origin)
		req.Header.Set("Sec-Fetch-Site", tt.site)
		response := httptest.NewRecorder()
		web.ServeHTTP(response, req)
		if response.Code != tt.want {
			t.Fatalf("request %#v returned %d: %s", tt, response.Code, response.Body)
		}
	}
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		req := httptest.NewRequest("GET", "http://"+web.host+path, nil)
		response := httptest.NewRecorder()
		web.ServeHTTP(response, req)
		if response.Code != 200 || response.Header().Get("Content-Security-Policy") == "" || strings.Contains(response.Body.String(), "private-auth-sentinel") {
			t.Fatalf("unsafe or missing static asset %s: %d", path, response.Code)
		}
	}
}

func TestWakeWebTaskLifecyclePreviewAndManualRequest(t *testing.T) {
	a, now, calls := newWakeTestApp(t)
	web := &wakeHTTP{app: a, token: "test-token", host: "127.0.0.1:7331"}
	body := `{"profile":"work","kind":"weekdays","times":"07:00,12:01,17:02","timezone":"Asia/Shanghai"}`
	preview := wakeWebRequest(t, web, "POST", "/api/preview", body)
	if preview.Code != 200 {
		t.Fatal(preview.Body)
	}
	var task wakeWebTask
	if err := json.Unmarshal(preview.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if len(task.Slots) != 3 || *calls != 0 {
		t.Fatalf("bad preview or preview sent a request: %#v", task)
	}
	state, _ := a.readWakeState()
	if len(state.Tasks) != 0 {
		t.Fatal("preview saved a task")
	}
	created := wakeWebRequest(t, web, "POST", "/api/tasks", body)
	if created.Code != 201 {
		t.Fatal(created.Body)
	}
	task = wakeWebTask{}
	_ = json.Unmarshal(created.Body.Bytes(), &task)
	path := "/api/tasks/" + task.ID
	if response := wakeWebRequest(t, web, "POST", path, `{"action":"pause"}`); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if onlyWakeTask(t, a).Enabled {
		t.Fatal("API pause did not persist")
	}
	*now = now.Add(time.Minute)
	updated := wakeWebRequest(t, web, "PUT", path, `{"profile":"work","kind":"interval","every":"5h1m","timezone":"UTC"}`)
	if updated.Code != 200 {
		t.Fatal(updated.Body)
	}
	saved := onlyWakeTask(t, a)
	if saved.Enabled || saved.ID != task.ID || saved.Schedule.Every != "5h1m" || !saved.NextRun.Equal(now.Add(5*time.Hour+time.Minute)) {
		t.Fatalf("edit lost identity, pause, or new anchor: %#v", saved)
	}
	if response := wakeWebRequest(t, web, "POST", path, `{"action":"resume"}`); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := wakeWebRequest(t, web, "POST", "/api/profiles/work/wake", `{}`); response.Code != 200 {
		t.Fatal(response.Body)
	}
	state, _ = a.readWakeState()
	if *calls != 1 || len(state.Tasks) != 1 || len(state.History) != 1 || state.History[0].Status != "success" || state.History[0].TaskID != "" {
		t.Fatalf("manual request created an unwanted plan or lost history: %#v", state)
	}
	if !state.Tasks[0].NextRun.Equal(saved.NextRun) {
		t.Fatal("profile wake moved scheduled time")
	}
	if response := wakeWebRequest(t, web, "DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	state, _ = a.readWakeState()
	if len(state.Tasks) != 0 || len(state.History) != 1 {
		t.Fatal("API removal discarded history or retained task")
	}
	for _, input := range []string{body + `{}`, `{"profile":"../work","kind":"daily","times":"07:00","timezone":"UTC"}`, `{"profile":"work","kind":"interval","every":"0s","timezone":"UTC"}`, `{"profile":"work","kind":"daily","times":"25:00","timezone":"UTC"}`} {
		if response := wakeWebRequest(t, web, "POST", "/api/tasks", input); response.Code != 400 {
			t.Fatalf("accepted invalid input %s: %s", input, response.Body)
		}
	}
}

func TestWakeWebPreviewTimesAndModelSettings(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	web := &wakeHTTP{app: a, token: "test-token", host: "127.0.0.1:7331"}
	body := `{"profile":"work","model":"gpt-6.1-terra","reasoning_effort":"low","kind":"daily","times":"05:00,12:01,17:02","timezone":"Asia/Shanghai"}`
	preview := wakeWebRequest(t, web, "POST", "/api/preview", body)
	var task wakeWebTask
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &task) != nil {
		t.Fatalf("preview failed: %s", preview.Body)
	}
	// At 08:00 Shanghai time, today's 05:00 has passed; it is the third future slot tomorrow.
	want := []string{"2026-10-28T04:01:00Z", "2026-10-28T09:02:00Z", "2026-10-28T21:00:00Z"}
	if len(task.Slots) != len(want) {
		t.Fatalf("unexpected preview slots: %v", task.Slots)
	}
	for i, value := range want {
		if !task.Slots[i].Equal(wakeTime(t, value)) {
			t.Fatalf("slot %d: got %s, want %s", i, task.Slots[i], value)
		}
	}
	created := wakeWebRequest(t, web, "POST", "/api/tasks", body)
	if created.Code != 201 {
		t.Fatal(created.Body)
	}
	saved := onlyWakeTask(t, a)
	if saved.Model != "gpt-6.1-terra" || saved.ReasoningEffort != "low" {
		t.Fatalf("model settings not saved: %#v", saved)
	}
	a.WakeRun = func(_ context.Context, _ string, args, _ []string, _ string, out io.Writer) error {
		joined := strings.Join(args, "|")
		if !strings.Contains(joined, "--model|gpt-6.1-terra") || !strings.Contains(joined, `model_reasoning_effort="low"`) {
			t.Fatalf("model settings not passed to Codex: %s", joined)
		}
		_, err := io.WriteString(out, "{\"type\":\"turn.completed\"}\n")
		return err
	}
	if err := a.executeWake(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
}

func TestWakeWebStateSeparatesLocalTokensFromQuota(t *testing.T) {
	a, now, _ := newWakeTestApp(t)
	sessions := filepath.Join(a.Paths.Profiles, "work", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-test.jsonl"), []any{
		usageEvent(now.Add(-time.Hour).Format(time.RFC3339Nano), usageFields(100, 40, 10, 0), usageFields(100, 40, 10, 0)),
	})
	web := &wakeHTTP{app: a, token: "test", host: "127.0.0.1:7331"}
	response := wakeWebRequest(t, web, "GET", "/api/state", "")
	var snapshot struct {
		Profiles []wakeWebProfile `json:"profiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Profiles) != 1 || snapshot.Profiles[0].Tokens != 110 || snapshot.Profiles[0].Requests != 1 || snapshot.Profiles[0].Quota != nil {
		t.Fatalf("local tokens were confused with quota: %s", response.Body)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private-auth-sentinel")) {
		t.Fatal("state leaked credentials")
	}
	var sum int64
	for _, tokens := range snapshot.Profiles[0].Activity {
		sum += tokens
	}
	if sum != 110 {
		t.Fatalf("activity bins do not match deduplicated usage: %d", sum)
	}
}

func TestWakeQuotaRPCUsesReadOnlyHandshakeAndCachesResult(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	root := t.TempDir()
	transcript, script := filepath.Join(root, "requests"), filepath.Join(root, "codex")
	// The fake CLI waits for initialize and initialized, then only accepts the
	// quota-read request. No model turn is available in this fixture.
	program := "#!/bin/sh\n" +
		"IFS= read -r first\nprintf '%s\\n' \"$first\" >> '" + transcript + "'\nprintf '%s\\n' '{\"id\":1,\"result\":{}}'\n" +
		"IFS= read -r second\nIFS= read -r third\nprintf '%s\\n' \"$second\" \"$third\" >> '" + transcript + "'\n" +
		"printf '%s\\n' '{\"id\":2,\"result\":{\"rateLimits\":{\"limitId\":\"codex\",\"primary\":{\"usedPercent\":28,\"windowDurationMins\":300,\"resetsAt\":1793152800},\"secondary\":null}}}'\nsleep 10\n"
	if err := os.WriteFile(script, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(string) (string, error) { return script, nil }
	quota, err := a.refreshWakeQuota(context.Background(), "work")
	if err != nil || quota.Buckets["codex"].Primary.UsedPercent != 28 {
		t.Fatalf("quota read: %#v, %v", quota, err)
	}
	requests, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, line := range bytes.Split(bytes.TrimSpace(requests), []byte{'\n'}) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, request.Method)
	}
	if strings.Join(methods, ",") != "initialize,initialized,account/rateLimits/read" {
		t.Fatalf("unexpected RPCs: %v", methods)
	}
	cached, err := a.readWakeQuotas()
	if err != nil || cached["work"].UpdatedAt.IsZero() || cached["work"].Buckets["codex"].Primary.WindowDurationMins != 300 {
		t.Fatalf("quota was not cached: %#v, %v", cached, err)
	}
	info, err := os.Stat(filepath.Join(a.wakeDir(), "quotas.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("quota cache is not private")
	}
	a.LookPath = func(string) (string, error) { return "/missing-codex", nil }
	if _, err := a.refreshWakeQuota(context.Background(), "work"); err == nil {
		t.Fatal("failed refresh was reported as success")
	}
	cached, _ = a.readWakeQuotas()
	if cached["work"].Error == "" || cached["work"].Buckets["codex"].Primary.UsedPercent != 28 {
		t.Fatal("failed refresh discarded last known quota")
	}
}

func TestWakeQuotaRPCDeadlineAndMultiBucketParsing(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(string) (string, error) { return path, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := a.refreshWakeQuota(ctx, "work"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("RPC deadline was not enforced: %v", err)
	}
	buckets, err := parseWakeQuota([]byte(`{"rateLimits":{"limitId":"old"},"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":42,"windowDurationMins":60,"resetsAt":1793152800}},"other":{"limitId":"other","primary":null,"secondary":null}}}`))
	if err != nil || len(buckets) != 2 || buckets["codex"].Primary.WindowDurationMins != 60 {
		t.Fatalf("multi-bucket data misread: %#v, %v", buckets, err)
	}
	unknown, err := parseWakeQuota([]byte(`{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":null,"resetsAt":null}}}`))
	if err != nil || unknown["codex"].Primary.WindowDurationMins != 0 || unknown["codex"].Primary.ResetsAt != 0 {
		t.Fatalf("nullable window details should be accepted: %#v, %v", unknown, err)
	}
	for _, invalid := range []string{`{}`, `{"rateLimits":{"primary":{"usedPercent":-5,"windowDurationMins":300}}}`, `{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":-1}}}`} {
		if _, err := parseWakeQuota([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid quota: %s", invalid)
		}
	}
}
