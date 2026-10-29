package cx

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"testing"
)

func TestWakeTeaEditPreservesIdentityAndPause(t *testing.T) {
	a, _, calls := newWakeTestApp(t)
	if err := a.addWake([]string{"work", "--daily", "05:00", "--tz", "UTC"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	a.changeWakeTask(task.ID, "pause")
	task = onlyWakeTask(t, a)
	m := &wakeTea{app: a, ctx: context.Background(), width: 80, height: 24}
	m.openForm(&task)
	m.fields[2].SetValue("06:00,11:01")
	m.fields[6].SetValue("gpt-6.1-terra")
	m.fields[7].SetValue("low")
	cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("save did not run")
	}
	if result := cmd().(wakeDone); result.err != nil {
		t.Fatal(result.err)
	}
	got := onlyWakeTask(t, a)
	if got.ID != task.ID || got.Enabled || got.ReasoningEffort != "low" || len(got.Schedule.Times) != 2 || *calls != 0 {
		t.Fatalf("bad saved task: %#v", got)
	}
}
func TestWakeWebDisabledUntilRequested(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	if a.wakeWebEnabled() {
		t.Fatal("web enabled by default")
	}
	if err := os.MkdirAll(a.wakeDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.wakeDir(), "web.enabled"), []byte("enabled"), 0600); err != nil {
		t.Fatal(err)
	}
	if !a.wakeWebEnabled() {
		t.Fatal("web setting ignored")
	}
	t.Setenv("CX_WAKE_NO_WEB", "1")
	if a.wakeWebEnabled() {
		t.Fatal("headless override ignored")
	}
}
