package cx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed wake_web/*
var wakeWebAssets embed.FS

type wakeWebAddress struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func (a *App) wakeWebPath() string { return filepath.Join(a.wakeDir(), "web.json") }

func (a *App) readWakeWeb() (wakeWebAddress, error) {
	var address wakeWebAddress
	data, err := os.ReadFile(a.wakeWebPath())
	if err != nil {
		return address, err
	}
	if err := json.Unmarshal(data, &address); err != nil {
		return address, err
	}
	u, err := url.Parse(address.URL)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || len(address.Token) != 64 || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return wakeWebAddress{}, errors.New("invalid local Web UI address; restart with 'cx wake uninstall' and 'cx wake install'")
	}
	return address, nil
}

func (a *App) wakeWebCommand(args []string) error {
	if len(args) != 0 {
		return wakeUsageError("use 'cx wake web'")
	}
	identity, err := wakeExecutableID()
	if err != nil {
		return err
	}
	available := func() (wakeWebAddress, string, bool) {
		address, err := a.readWakeWeb()
		if err != nil {
			return address, "", false
		}
		client := http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequest(http.MethodGet, address.URL+"/api/health", nil)
		req.Header.Set("Authorization", "Bearer "+address.Token)
		resp, err := client.Do(req)
		if err != nil {
			return address, "", false
		}
		defer resp.Body.Close()
		var result struct {
			OK      bool   `json:"ok"`
			BuildID string `json:"build_id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result)
		return address, result.BuildID, err == nil && resp.StatusCode == http.StatusOK && result.OK
	}
	if address, buildID, ok := available(); ok && buildID == identity {
		_, err := fmt.Fprintln(a.Stdout, address.URL+"/#token="+address.Token)
		return err
	} else if ok {
		if err := a.upgradeWakeWeb(address, identity, available); err != nil {
			return err
		}
	} else {
		if err := a.wakeService([]string{"install"}); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if address, buildID, ok := available(); ok && buildID == identity {
			_, err := fmt.Fprintln(a.Stdout, address.URL+"/#token="+address.Token)
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("Web UI has not started; inspect 'cx wake status' and the scheduler log")
}

func wakeExecutableID() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (a *App) startWakeWeb(ctx context.Context) (func(), <-chan error, error) {
	buildID, err := wakeExecutableID()
	if err != nil {
		return nil, nil, err
	}
	lock, err := a.lockWake("web", true)
	if err != nil {
		return nil, nil, errors.New("a Web UI is already running for this profile store; use 'cx wake web'")
	}
	bind := os.Getenv("CX_WAKE_WEB_ADDR")
	if bind == "" {
		bind = "127.0.0.1:7331"
	}
	host, port, err := net.SplitHostPort(bind)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 0 || portNumber > 65535 || !net.ParseIP(host).IsLoopback() {
		unlockWake(lock)
		return nil, nil, errors.New("CX_WAKE_WEB_ADDR must be a loopback IP and port, for example 127.0.0.1:7331")
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil && os.Getenv("CX_WAKE_WEB_ADDR") == "" && errors.Is(err, syscall.EADDRINUSE) {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		unlockWake(lock)
		return nil, nil, fmt.Errorf("start local Web UI: %w", err)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		listener.Close()
		unlockWake(lock)
		return nil, nil, err
	}
	address := wakeWebAddress{URL: "http://" + listener.Addr().String(), Token: hex.EncodeToString(secret[:]), PID: os.Getpid()}
	data, _ := json.Marshal(address)
	if err := writeFileAtomic(a.wakeWebPath(), append(data, '\n'), 0o600); err != nil {
		listener.Close()
		unlockWake(lock)
		return nil, nil, err
	}
	web := &wakeHTTP{app: a, token: address.Token, host: listener.Addr().String(), buildID: buildID}
	server := &http.Server{Handler: web, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, WriteTimeout: 2 * time.Minute,
		MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	errors := make(chan error, 1)
	go func() { errors <- server.Serve(listener) }()
	stop := func() {
		_ = server.Close()
		if current, err := a.readWakeWeb(); err == nil && current.Token == address.Token {
			_ = os.Remove(a.wakeWebPath())
		}
		unlockWake(lock)
	}
	_, _ = fmt.Fprintf(a.Stdout, "Web UI: %s (run 'cx wake web' for the access link)\n", address.URL)
	return stop, errors, nil
}

type wakeHTTP struct {
	app     *App
	token   string
	host    string
	buildID string
	usageMu sync.Mutex
	usageAt time.Time
	usage   []wakeWebProfile
}

func (s *wakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != s.host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeWakeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			writeWakeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请运行 cx wake web 获取新的访问链接"})
			return
		}
		s.api(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	files := map[string]string{"/": "index.html", "/app.js": "app.js", "/app.css": "app.css"}
	file, ok := files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := wakeWebAssets.ReadFile("wake_web/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "app.css": "text/css; charset=utf-8"}
	w.Header().Set("Content-Type", contentType[file])
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func writeWakeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeWakeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media != "application/json" {
		return errors.New("request must be JSON")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("请求内容无效或过长")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
