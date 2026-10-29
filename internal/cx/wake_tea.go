package cx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

var wakeTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("72"))
var wakeSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))

func (a *App) wakeInteractive(reader *bufio.Reader) error {
	// Keep redirected input usable for scripts and the existing line menu.
	if f, ok := a.Stdin.(*os.File); !ok || !term.IsTerminal(f.Fd()) {
		return a.wakeMenu(reader)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &wakeTea{app: a, ctx: ctx, width: 80, height: 24}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(a.Stdin), tea.WithOutput(a.Stdout)).Run()
	return err
}

type wakeSnapshot struct {
	state    wakeState
	profiles []wakeWebProfile
	err      error
}
type wakeDone struct{ err error }
type wakeTick time.Time
type wakeTea struct {
	app                        *App
	ctx                        context.Context
	state                      wakeState
	profiles                   []wakeWebProfile
	tab, cursor, width, height int
	busy                       bool
	notice                     string
	fields                     []textinput.Model
	focus                      int
	editing                    string
	enabled                    bool
	confirm                    string
	action                     func(*App) error
}

func (m *wakeTea) load() tea.Cmd {
	return func() tea.Msg {
		s, err := m.app.readWakeState()
		if err != nil {
			return wakeSnapshot{err: err}
		}
		p, err := (&wakeHTTP{app: m.app}).profiles(m.app.Now())
		return wakeSnapshot{s, p, err}
	}
}
func wakeTimer() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return wakeTick(t) })
}
func (m *wakeTea) Init() tea.Cmd { return tea.Batch(m.load(), wakeTimer()) }
func (m *wakeTea) run(fn func(*App) error) tea.Cmd {
	m.busy = true
	m.notice = "处理中…"
	return func() tea.Msg { a := *m.app; a.Stdout = io.Discard; a.Stderr = io.Discard; return wakeDone{fn(&a)} }
}
func (m *wakeTea) count() int {
	switch m.tab {
	case 0:
		return len(m.state.Tasks)
	case 1:
		return len(m.profiles)
	case 2:
		return len(m.state.History)
	default:
		return 4
	}
}
func (m *wakeTea) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
	case wakeSnapshot:
		if v.err != nil {
			m.notice = v.err.Error()
		} else {
			m.state = v.state
			m.profiles = v.profiles
		}
		if m.cursor >= m.count() {
			m.cursor = max(0, m.count()-1)
		}
	case wakeDone:
		m.busy = false
		if v.err != nil {
			m.notice = v.err.Error()
		} else {
			m.notice = "已完成"
		}
		return m, m.load()
	case wakeTick:
		if m.busy || len(m.fields) > 0 {
			return m, wakeTimer()
		}
		return m, tea.Batch(m.load(), wakeTimer())
	case tea.KeyMsg:
		key := v.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if m.confirm != "" {
			fn := m.action
			m.confirm = ""
			m.action = nil
			if key == "y" || key == "Y" {
				return m, m.run(fn)
			}
			return m, nil
		}
		if len(m.fields) > 0 {
			return m, m.updateForm(v)
		}
		switch key {
		case "q", "esc":
			return m, tea.Quit
		case "tab", "right":
			m.tab = (m.tab + 1) % 4
			m.cursor = 0
		case "shift+tab", "left":
			m.tab = (m.tab + 3) % 4
			m.cursor = 0
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(max(0, m.count()-1), m.cursor+1)
		case "r":
			return m, m.load()
		case "n":
			m.openForm(nil)
			return m, textinput.Blink
		case "e", "enter":
			if m.tab == 0 && len(m.state.Tasks) > 0 {
				t := m.state.Tasks[m.cursor]
				if t.LastStatus == "running" {
					m.notice = "请求正在执行，完成后可编辑"
				} else {
					m.openForm(&t)
					return m, textinput.Blink
				}
			}
			if m.tab == 3 {
				return m, m.background()
			}
		case "p":
			if m.tab == 0 && len(m.state.Tasks) > 0 {
				t := m.state.Tasks[m.cursor]
				act := "resume"
				if t.Enabled {
					act = "pause"
				}
				return m, m.run(func(a *App) error { return a.changeWakeTask(t.ID, act) })
			}
		case "d":
			if m.tab == 0 && len(m.state.Tasks) > 0 {
				t := m.state.Tasks[m.cursor]
				m.confirm = "删除计划 " + t.ID + "？历史会保留。 [y/N]"
				m.action = func(a *App) error { return a.changeWakeTask(t.ID, "remove") }
			}
		case "w":
			if m.tab == 0 && len(m.state.Tasks) > 0 {
				t := m.state.Tasks[m.cursor]
				m.confirm = "立即发送一次请求，将消耗额度。 [y/N]"
				m.action = func(a *App) error { return a.executeWake(m.ctx, t.ID) }
			}
			if m.tab == 1 && len(m.profiles) > 0 {
				p := m.profiles[m.cursor]
				m.confirm = "唤醒 " + p.Name + "，将消耗额度。 [y/N]"
				m.action = func(a *App) error { return a.wakeProfileNow(m.ctx, p.Name, "gpt-6.1-terra") }
			}
		case "f":
			if m.tab == 1 && len(m.profiles) > 0 {
				p := m.profiles[m.cursor]
				return m, m.run(func(a *App) error { _, err := a.refreshWakeQuota(m.ctx, p.Name); return err })
			}
		case "F":
			if m.tab == 1 {
				profiles := append([]wakeWebProfile(nil), m.profiles...)
				return m, m.run(func(a *App) error {
					for _, p := range profiles {
						if _, err := a.refreshWakeQuota(m.ctx, p.Name); err != nil {
							return err
						}
					}
					return nil
				})
			}
		case "P", "R":
			if m.tab == 0 {
				tasks := append([]wakeTask(nil), m.state.Tasks...)
				return m, m.run(func(a *App) error {
					for _, t := range tasks {
						act := "pause"
						if key == "R" {
							act = "resume"
							if t.Schedule.Kind == "once" && t.NextRun.IsZero() {
								continue
							}
						}
						if err := a.changeWakeTask(t.ID, act); err != nil {
							return err
						}
					}
					return nil
				})
			}
		}
	}
	return m, nil
}
func (m *wakeTea) background() tea.Cmd {
	switch m.cursor {
	case 0:
		return m.run(func(a *App) error { return a.wakeService([]string{"install"}) })
	case 1:
		m.confirm = "停止后台调度？计划和历史保留。 [y/N]"
		m.action = func(a *App) error { return a.wakeService([]string{"uninstall"}) }
	case 2:
		return m.run(func(a *App) error { return a.wakeWebCommand(nil) })
	case 3:
		return m.run(func(a *App) error { return a.wakeWebCommand([]string{"off"}) })
	}
	return nil
}

var wakeFieldNames = []string{"账号", "方式 (daily / weekdays / interval / once)", "每日时间", "单次时间 (RFC3339 或 YYYY-MM-DD HH:MM)", "间隔 (如 5h1m)", "时区", "模型 (留空使用 CLI 默认)", "推理强度 (low / medium / high / xhigh / max / ultra)"}

func (m *wakeTea) openForm(t *wakeTask) {
	profile := ""
	if len(m.profiles) > 0 {
		profile = m.profiles[0].Name
	}
	values := []string{profile, "daily", "05:00,11:01,17:10", "", "5h1m", "Local", "gpt-6.1-terra", "low"}
	m.editing = ""
	m.enabled = true
	if t != nil {
		m.editing = t.ID
		m.enabled = t.Enabled
		values = []string{t.Profile, t.Schedule.Kind, strings.Join(t.Schedule.Times, ","), "", t.Schedule.Every, t.Schedule.Timezone, t.Model, t.ReasoningEffort}
		if !t.Schedule.At.IsZero() {
			values[3] = t.Schedule.At.Format(time.RFC3339)
		}
	}
	m.fields = make([]textinput.Model, len(values))
	for i, v := range values {
		f := textinput.New()
		f.SetValue(v)
		f.CharLimit = 200
		f.Width = max(15, min(65, m.width-8))
		m.fields[i] = f
	}
	m.focus = 0
	m.fields[0].Focus()
	m.notice = ""
}
func (m *wakeTea) input() wakeTaskInput {
	return wakeTaskInput{Profile: m.fields[0].Value(), Kind: m.fields[1].Value(), Times: m.fields[2].Value(), At: m.fields[3].Value(), Every: m.fields[4].Value(), Timezone: m.fields[5].Value(), Model: m.fields[6].Value(), ReasoningEffort: m.fields[7].Value()}
}
func (m *wakeTea) updateForm(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.fields = nil
		return nil
	case "tab", "enter", "down", "shift+tab", "up":
		m.fields[m.focus].Blur()
		step := 1
		if k.String() == "shift+tab" || k.String() == "up" {
			step = -1
		}
		m.focus = (m.focus + step + len(m.fields)) % len(m.fields)
		return m.fields[m.focus].Focus()
	case "ctrl+s":
		t, err := (&wakeHTTP{app: m.app}).parseTask(m.input())
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		t.Enabled = m.enabled
		id := m.editing
		m.fields = nil
		return m.run(func(a *App) error {
			if id != "" {
				t.ID = id
				return a.updateWakeTask(t, nil)
			}
			return a.saveNewWakeTask(t)
		})
	}
	var cmd tea.Cmd
	m.fields[m.focus], cmd = m.fields[m.focus].Update(k)
	return cmd
}
func (m *wakeTea) View() string {
	var b strings.Builder
	b.WriteString(wakeTitleStyle.Render("cx wake") + "\n\n")
	if len(m.fields) > 0 {
		start := 0
		if m.height < 30 {
			start = max(0, m.focus-2)
		}
		end := min(len(m.fields), start+max(1, (m.height-11)/2))
		for i := start; i < end; i++ {
			b.WriteString(wakeFieldNames[i] + "\n" + m.fields[i].View() + "\n")
		}
		if task, err := (&wakeHTTP{app: m.app}).parseTask(m.input()); err == nil {
			if task.Schedule.Kind == "daily" || task.Schedule.Kind == "weekdays" {
				b.WriteString("\n同一天的 +5h 参考: " + strings.Join(task.Schedule.Times, " / ") + "\n")
			}
			b.WriteString("下次: " + formatWakeTime(task.NextRun, task.Schedule) + "\n窗口参考结束: " + formatWakeTime(task.NextRun.Add(5*time.Hour), task.Schedule) + "\n")
		}
		b.WriteString("\nTab/↑↓ 切换字段 · Ctrl+S 保存 · Esc 取消\n")
	} else {
		for i, s := range []string{"计划", "账号 / 额度", "历史", "后台"} {
			if i == m.tab {
				s = wakeSelectedStyle.Render("[" + s + "]")
			}
			b.WriteString(s + "   ")
		}
		b.WriteString("\n\n")
		var rows []string
		switch m.tab {
		case 0:
			for _, t := range m.state.Tasks {
				state := "暂停"
				if t.Enabled {
					state = "启用"
				}
				rows = append(rows, fmt.Sprintf("%s · %s · %s\n  %s · %s %s\n  下次 %s · 最近 %s", t.Profile, state, t.Schedule, t.ID, t.Model, t.ReasoningEffort, formatWakeTime(t.NextRun, t.Schedule), t.LastStatus))
			}
		case 1:
			for _, p := range m.profiles {
				s := fmt.Sprintf("%s · 本地 5h: %d 次 / %d tokens", p.Name, p.Requests, p.Tokens)
				if p.Quota == nil || p.Quota.UpdatedAt.IsZero() {
					s += "\n  额度尚未查询，按 f 刷新"
				} else {
					s += "\n  查询于 " + p.Quota.UpdatedAt.Local().Format(time.RFC3339)
					for _, id := range sortedQuotaKeys(p.Quota.Buckets) {
						q := p.Quota.Buckets[id]
						for _, w := range []*wakeQuotaWindow{q.Primary, q.Secondary} {
							if w != nil {
								reset := "未知"
								if w.ResetsAt > 0 {
									reset = time.Unix(w.ResetsAt, 0).Local().Format("01-02 15:04")
								}
								s += fmt.Sprintf("\n  %s · %dmin · 已用 %.0f%% · 重置 %s", id, w.WindowDurationMins, w.UsedPercent, reset)
							}
						}
					}
				}
				if p.Quota != nil && p.Quota.Error != "" {
					s += "\n  " + p.Quota.Error
				}
				rows = append(rows, s)
			}
		case 2:
			for i := len(m.state.History) - 1; i >= 0; i-- {
				r := m.state.History[i]
				rows = append(rows, fmt.Sprintf("%s · %s · %s\n  %s", r.StartedAt.Local().Format("01-02 15:04"), r.Profile, r.Status, r.Message))
			}
		case 3:
			rows = []string{"安装 / 更新后台调度", "停止后台调度", "启用 Web（链接可通过 cx wake web 获取）", "关闭 Web"}
		}
		if len(rows) == 0 {
			b.WriteString("暂无记录\n")
		}
		start := m.cursor
		used := 0
		limit := max(3, m.height-12)
		for i := start; i < len(rows); i++ {
			row := rows[i]
			n := strings.Count(row, "\n") + 2
			if used+n > limit && i > start {
				break
			}
			if i == m.cursor {
				row = wakeSelectedStyle.Render("› " + row)
			} else {
				row = "  " + row
			}
			b.WriteString(row + "\n\n")
			used += n
		}
		b.WriteString("Tab/←→ 切页 · ↑↓ 选择 · r 刷新 · q 退出\n")
		switch m.tab {
		case 0:
			b.WriteString("n 新建 · e/Enter 编辑 · p 暂停/恢复 · d 删除 · w 唤醒 · P/R 全部暂停/恢复\n")
		case 1:
			b.WriteString("f 刷新额度 · F 刷新全部 · w 唤醒\n")
		case 3:
			b.WriteString("Enter 执行所选操作\n")
		}
	}
	if m.confirm != "" {
		b.WriteString("\n" + m.confirm)
	} else if m.notice != "" {
		b.WriteString("\n" + m.notice)
	}
	return lipgloss.NewStyle().MaxWidth(max(20, m.width-2)).Render(b.String())
}

func sortedQuotaKeys(b map[string]wakeQuotaBucket) []string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
