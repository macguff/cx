package cx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type wakeQuotaWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type wakeQuotaBucket struct {
	LimitID   string           `json:"limitId"`
	LimitName string           `json:"limitName"`
	PlanType  string           `json:"planType"`
	Primary   *wakeQuotaWindow `json:"primary"`
	Secondary *wakeQuotaWindow `json:"secondary"`
}

type wakeQuota struct {
	UpdatedAt time.Time                  `json:"updated_at"`
	AttemptAt time.Time                  `json:"attempt_at"`
	Buckets   map[string]wakeQuotaBucket `json:"buckets"`
	Error     string                     `json:"error,omitempty"`
}

func (a *App) readWakeQuotas() (map[string]wakeQuota, error) {
	data, err := os.ReadFile(filepath.Join(a.wakeDir(), "quotas.json"))
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]wakeQuota), nil
	}
	if err != nil {
		return nil, err
	}
	var quotas map[string]wakeQuota
	if err := json.Unmarshal(data, &quotas); err != nil {
		return nil, fmt.Errorf("read quota cache: %w", err)
	}
	if quotas == nil {
		quotas = make(map[string]wakeQuota)
	}
	return quotas, nil
}

func (a *App) refreshWakeQuota(ctx context.Context, profile string) (wakeQuota, error) {
	if _, err := a.existingProfile(profile); err != nil {
		return wakeQuota{}, err
	}
	// Deduplicate refreshes per profile, including across HTTP clients/processes.
	lock, err := a.lockWake("quota-"+profile, true)
	if err != nil {
		return wakeQuota{}, errors.New("quota refresh is already in progress for this profile")
	}
	defer unlockWake(lock)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	buckets, queryErr := a.queryWakeQuota(ctx, profile)
	cacheLock, err := a.lockWake("quotas", false)
	if err != nil {
		return wakeQuota{}, err
	}
	defer unlockWake(cacheLock)
	quotas, err := a.readWakeQuotas()
	if err != nil {
		return wakeQuota{}, err
	}
	quota := quotas[profile]
	quota.AttemptAt = a.Now()
	if queryErr != nil {
		quota.Error = queryErr.Error()
	} else {
		quota.Buckets, quota.UpdatedAt, quota.Error = buckets, a.Now(), ""
	}
	quotas[profile] = quota
	data, err := json.Marshal(quotas)
	if err != nil {
		return wakeQuota{}, err
	}
	if err := writeFileAtomic(filepath.Join(a.wakeDir(), "quotas.json"), append(data, '\n'), 0o600); err != nil {
		return wakeQuota{}, err
	}
	return quota, queryErr
}

// Uses only the official account/rateLimits/read RPC. No threads, model turns,
// raw authentication data, earned-reset redemption, or direct upstream HTTP.
func (a *App) queryWakeQuota(ctx context.Context, profile string) (map[string]wakeQuotaBucket, error) {
	home, err := a.existingProfile(profile)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Join(home, "auth.json"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("请先使用 cx login 登录这个账号")
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return nil, errors.New("找不到 Codex CLI，请检查安装和 PATH")
	}
	cmd := exec.CommandContext(ctx, codex, "app-server", "--listen", "stdio://", "-c", `cli_auth_credentials_store="file"`)
	cmd.Env, cmd.Dir, cmd.Stderr = wakeEnvironment(os.Environ(), home), a.wakeDir(), io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, errors.New("无法启动 Codex app-server，请更新 Codex CLI")
	}
	defer func() { _ = input.Close(); _ = cmd.Cancel(); _ = cmd.Wait() }()
	encoder := json.NewEncoder(input)
	if err := encoder.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "cx", "title": "cx Wake", "version": BuildVersion}}}); err != nil {
		return nil, errors.New("无法初始化 Codex app-server")
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID == 0 {
			continue
		}
		if len(message.Error) > 0 && string(message.Error) != "null" {
			return nil, errors.New("额度查询失败：请检查账号登录、网络与 Codex CLI 版本；API Key 账号可能没有套餐额度")
		}
		if message.ID == 1 {
			if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
				return nil, errors.New("无法完成 Codex 初始化")
			}
			if err := encoder.Encode(map[string]any{"id": 2, "method": "account/rateLimits/read"}); err != nil {
				return nil, errors.New("无法提交额度查询")
			}
		} else if message.ID == 2 {
			return parseWakeQuota(message.Result)
		}
	}
	if ctx.Err() != nil {
		return nil, errors.New("额度查询超时或被取消，请稍后重试")
	}
	return nil, errors.New("Codex 未返回额度数据，请检查登录状态和 CLI 版本")
}

func parseWakeQuota(data []byte) (map[string]wakeQuotaBucket, error) {
	var result struct {
		Single  *wakeQuotaBucket           `json:"rateLimits"`
		Buckets map[string]wakeQuotaBucket `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("Codex 返回了无法解析的额度数据")
	}
	if len(result.Buckets) == 0 && result.Single != nil {
		id := result.Single.LimitID
		if id == "" {
			id = "codex"
		}
		result.Buckets = map[string]wakeQuotaBucket{id: *result.Single}
	}
	if len(result.Buckets) == 0 {
		return nil, errors.New("当前账号未返回套餐额度窗口")
	}
	for _, bucket := range result.Buckets {
		for _, window := range []*wakeQuotaWindow{bucket.Primary, bucket.Secondary} {
			// The app-server protocol allows an unknown duration or reset time.
			// JSON null decodes to zero, which the UI displays as unavailable.
			if window != nil && (math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 || window.WindowDurationMins < 0 || window.ResetsAt < 0) {
				return nil, errors.New("Codex 返回的额度窗口不完整")
			}
		}
	}
	return result.Buckets, nil
}
