package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

func TestFinalFixDefaultSelectionAndPlainHistoryIncludeOlderRunning(t *testing.T) {
	c := &testConn{done: make(chan struct{})}
	a, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil })
	s := client.Snapshot{State: "online"}
	for i := 0; i < 20; i++ {
		s.Tasks = append(s.Tasks, worker.LocalTaskView{Task: protocol.Task{ID: fmt.Sprintf("queued_%02d", i), State: protocol.Queued}, PackageName: fmt.Sprintf("排队软件%02d", i)})
	}
	s.Tasks = append(s.Tasks, worker.LocalTaskView{Task: protocol.Task{ID: "running_old", State: protocol.TaskRunning}, PackageName: "较早活动任务", Progress: events.Progress{Stage: "downloading", CompletedBytes: 1, TotalBytes: 2}})
	c.tasks = s.Tasks
	if err := a.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-inert")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for a.Snapshot().State != "online" {
		if time.Now().After(deadline) {
			t.Fatal("not online")
		}
		time.Sleep(time.Millisecond)
	}
	m := newModel(a)
	m.Update(snapshotMsg(s))
	if v, ok := m.selectedTask(); !ok || v.Task.ID != "running_old" || m.taskLimit() != 20 {
		t.Fatalf("default cancel target %s / limit %d", v.Task.ID, m.taskLimit())
	}
	body := strings.Join(m.dashboard(), "\n")
	if !strings.Contains(body, "20. 较早活动任务 · 下载 50%") || strings.Contains(body, "等待任务状态更新") {
		t.Fatal(body)
	}
	m.navigate(-1)
	selected := m.selected
	m.Update(snapshotMsg(s))
	if m.selected != selected {
		t.Fatal("refresh changed explicitly selected task")
	}
	m.navigate(1)
	if m.selected != "running_old" {
		t.Fatal("active task not navigable")
	}
	apply(m, key(m, runeKey("c")))
	c.mu.Lock()
	cancelled := c.cancel
	c.mu.Unlock()
	if cancelled != "running_old" {
		t.Fatalf("TUI cancelled %s", cancelled)
	}
	var out bytes.Buffer
	p := plainReporter{}
	p.history(&out, s, 20)
	if !strings.Contains(out.String(), "较早活动任务 · 下载 50%") || len(p.ids) != 20 {
		t.Fatal(out.String())
	}
	number := p.numbers["running_old"]
	s.Tasks = append([]worker.LocalTaskView{{Task: protocol.Task{ID: "new", State: protocol.Queued}}}, s.Tasks...)
	p.history(&out, s, 20)
	if p.numbers["running_old"] != number || p.ids[number-1] != "running_old" {
		t.Fatal("plain number rebound")
	}
	c.mu.Lock()
	c.cancel = ""
	c.mu.Unlock()
	p.taskCommand(a, &out, s, []string{"c", fmt.Sprint(number)})
	c.mu.Lock()
	cancelled = c.cancel
	c.mu.Unlock()
	if cancelled != "running_old" {
		t.Fatalf("plain cancelled %s", cancelled)
	}
	s.Tasks[len(s.Tasks)-1].Progress.Stage = "cancelling"
	out.Reset()
	p.history(&out, s, 20)
	if !strings.Contains(out.String(), "较早活动任务 · 正在取消任务") {
		t.Fatal(out.String())
	}
}

func TestFinalFixHistoryUsesInspectionResult(t *testing.T) {
	a, _ := fixtureApp(t, nil)
	m := newModel(a)
	m.snapshot = client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "inspect", Spec: protocol.TaskSpec{Type: protocol.InspectSystem}, State: protocol.Succeeded, Result: &protocol.TaskResult{Verified: true}}}}}
	m.ensureSelection()
	if body := strings.Join(m.dashboard(), "\n"); strings.Contains(body, "安装版本") || !strings.Contains(body, "1. 系统检查 · 系统检查已完成") {
		t.Fatal(body)
	}
	var out bytes.Buffer
	p := plainReporter{}
	p.history(&out, m.snapshot, 20)
	if strings.Contains(out.String(), "安装版本") || !strings.Contains(out.String(), "系统检查已完成") {
		t.Fatal(out.String())
	}
}
