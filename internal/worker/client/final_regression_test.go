package client

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"remoteops/internal/worker"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

func TestFinalFixSnapshotRetainsRunningTaskAndUpdatesNotice(t *testing.T) {
	b := pairingFixture(t)
	c := newConnection(b)
	for i := 0; i < 100; i++ {
		c.tasks = append(c.tasks, worker.LocalTaskView{Task: protocol.Task{ID: fmt.Sprintf("queued_%03d", i), State: protocol.Queued}})
	}
	c.tasks = append(c.tasks, worker.LocalTaskView{Task: protocol.Task{ID: "running_old", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "downloading", CompletedBytes: 1, TotalBytes: 2}})
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
	t.Cleanup(func() { a.Disconnect("local_closed"); a.Wait() })
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-inert")); err != nil {
		t.Fatal(err)
	}
	s := awaitState(t, a, "online")
	v, ok := PrimaryTask(s.Tasks)
	if !ok || v.Task.ID != "running_old" || len(s.Tasks) != 100 {
		t.Fatalf("current task omitted from bounded snapshot: %s / %d", v.Task.ID, len(s.Tasks))
	}
	if strings.Contains(s.Notice, "等待控制方") || !strings.Contains(s.Notice, "下载") {
		t.Fatalf("active notice: %s", s.Notice)
	}
	if err := a.CancelTask(v.Task.ID); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	cancelled := c.cancelled
	c.tasks = nil
	c.mu.Unlock()
	if cancelled != "running_old" {
		t.Fatalf("cancelled %s", cancelled)
	}
	if idle := a.Snapshot().Notice; idle != "已连接，等待控制方安排任务" {
		t.Fatal(idle)
	}
}

func TestFinalFixSharedTaskCopy(t *testing.T) {
	cases := []struct {
		state       protocol.TaskState
		kind        protocol.TaskType
		stage, want string
		result      *protocol.TaskResult
	}{
		{protocol.Succeeded, protocol.InspectSystem, "succeeded", "系统检查已完成", &protocol.TaskResult{Verified: true}},
		{protocol.Succeeded, protocol.InstallPackage, "succeeded", "安装版本已验证：2.51.0；需要手动重启", &protocol.TaskResult{Verified: true, Version: "2.51.0", RebootRequired: true}},
		{protocol.TaskRunning, protocol.InstallPackage, "downloading", "下载 50%", nil},
		{protocol.TaskRunning, protocol.InstallPackage, "cancelling", "正在取消任务", nil},
		{protocol.Queued, protocol.InstallPackage, "cancelling", "正在取消任务", nil},
		{protocol.TaskRunning, protocol.InspectSystem, "", "正在执行任务", nil},
		{protocol.Cancelled, protocol.InstallPackage, "downloading", "任务已取消", nil},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			v := worker.LocalTaskView{Task: protocol.Task{State: tc.state, Spec: protocol.TaskSpec{Type: tc.kind}, Result: tc.result}, Progress: events.Progress{Stage: tc.stage, CompletedBytes: 1, TotalBytes: 2}}
			if got := TaskResultText(v); !strings.Contains(got, tc.want) {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
