package client

import (
	"errors"
	"remoteops/internal/worker"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
)

func TestProgressOnlyDownloadsHaveRatios(t *testing.T) {
	if got := ProgressText(events.Progress{Stage: "downloading", CompletedBytes: 1 << 20, TotalBytes: 2 << 20}); !strings.Contains(got, "50%") || !strings.Contains(got, "1.0 / 2.0 MB") {
		t.Fatal(got)
	}
	if got := ProgressText(events.Progress{Stage: "downloading", CompletedBytes: 1 << 20}); got != "已下载 1.0 MB" {
		t.Fatal(got)
	}
	if got := ProgressText(events.Progress{Stage: "installing", CompletedBytes: 50, TotalBytes: 100}); strings.Contains(got, "%") || !strings.Contains(got, "安装") {
		t.Fatal(got)
	}
	for _, stage := range []string{"queued", "inspecting", "verifying_artifact", "awaiting_uac", "verifying_version", "cancelling", "succeeded", "failed", "cancelled", "timed_out", "unknown"} {
		if StageText(stage) == stage || StageText(stage) == "" {
			t.Fatalf("untranslated %s", stage)
		}
	}
	if got := StageText("tskey-auth-private\x1b"); strings.Contains(got, "private") {
		t.Fatal(got)
	}
}

func TestTaskTitleUsesCatalogueNameAndSanitizes(t *testing.T) {
	if got := TaskTitle(worker.LocalTaskView{Task: protocol.Task{Spec: protocol.TaskSpec{Type: protocol.InspectSystem}}}); got != "系统检查" {
		t.Fatal(got)
	}
	v := worker.LocalTaskView{PackageName: "Git\x1b", Task: protocol.Task{Spec: protocol.TaskSpec{Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "opaque-id", Version: "2.51.0"}}}}
	if got := TaskTitle(v); got != "Git 2.51.0" {
		t.Fatal(got)
	}
	v.PackageName = "tskey-auth-private"
	if got := TaskTitle(v); strings.Contains(got, "private") {
		t.Fatal(got)
	}
	if got := ErrorText(errors.New("opaque-secret tskey-auth-secret token=secret")); strings.Contains(got, "secret") || !strings.Contains(got, "重新") {
		t.Fatal(got)
	}
	if ErrorText(nil) != "" {
		t.Fatal("nil error displays failure")
	}
}

func TestSharedPresentationKeepsTasksSeparateAndShowsResults(t *testing.T) {
	tasks := []worker.LocalTaskView{{Task: protocol.Task{ID: "finished", State: protocol.Succeeded}}, {Task: protocol.Task{ID: "queued", State: protocol.Queued}}, {Task: protocol.Task{ID: "running", State: protocol.TaskRunning}}}
	if got, ok := PrimaryTask(tasks); !ok || got.Task.ID != "running" {
		t.Fatal("active task not selected")
	}
	if got, ok := PrimaryTask(tasks[:2]); !ok || got.Task.ID != "queued" {
		t.Fatal("queued task not selected")
	}
	if got, ok := PrimaryTask(tasks[:1]); !ok || got.Task.ID != "finished" {
		t.Fatal("recent result not selected")
	}
	if _, ok := PrimaryTask(nil); ok {
		t.Fatal("empty list selects task")
	}
	for _, state := range []string{"idle", "ready", "connecting", "online", "disconnected", "error"} {
		if StateText(state) == state || StateText(state) == "" {
			t.Fatal(state)
		}
	}
	if got := SafeText("\x1btoken=secret\n"); strings.Contains(got, "secret") || strings.ContainsAny(got, "\x1b\n") {
		t.Fatal(got)
	}
	v := worker.LocalTaskView{Task: protocol.Task{State: protocol.Succeeded, Result: &protocol.TaskResult{Verified: true, Version: "2.51.0", RebootRequired: true}}}
	if got := TaskResultText(v); !strings.Contains(got, "2.51.0") || !strings.Contains(got, "手动重启") {
		t.Fatal(got)
	}
	v.Task.State = protocol.TaskFailed
	v.Task.ErrorMessage = "tskey-auth-secret"
	if got := TaskResultText(v); strings.Contains(got, "secret") || !strings.Contains(got, "控制方") {
		t.Fatal(got)
	}
}
