package gui

import (
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestLayoutFitsMinimumAndDPI(t *testing.T) {
	for _, dpi := range []int{96, 144, 192} {
		w, h := scale(704, dpi), scale(521, dpi)
		for _, connected := range []bool{false, true} {
			for _, logs := range []bool{false, true} {
				l := makeLayout(w, h, dpi, connected, logs)
				for id, a := range l {
					if a.X < 0 || a.Y < 90*dpi/96 || a.W <= 0 || a.H <= 0 || a.X+a.W > w || a.Y+a.H > h {
						t.Fatalf("dpi %d id %d escapes: %+v", dpi, id, a)
					}
					for other, b := range l {
						if id < other && a.overlaps(b) {
							t.Fatalf("dpi %d controls %d and %d overlap", dpi, id, other)
						}
					}
				}
			}
		}
	}
}
func TestConsentAndExpiryGateConnection(t *testing.T) {
	now := time.Now()
	s := client.Snapshot{State: "ready", PairingExpiresAt: now.Add(time.Minute)}
	if canConnect(s, false, false, now) {
		t.Fatal("unchecked consent permits connect")
	}
	if !canConnect(s, true, false, now) {
		t.Fatal("valid consent rejected")
	}
	s.PairingExpiresAt = now
	if canConnect(s, true, false, now) {
		t.Fatal("expired pairing permits connect")
	}
}
func TestSelectedTaskDoesNotFollowOtherTaskProgress(t *testing.T) {
	tasks := []worker.LocalTaskView{{Task: protocol.Task{ID: "a", State: "running"}}, {Task: protocol.Task{ID: "b", State: "queued"}}}
	if v, ok := selectedTask(tasks, "b"); !ok || v.Task.ID != "b" {
		t.Fatal("selection crossed task IDs")
	}
	if _, ok := selectedTask(tasks, "gone"); ok {
		t.Fatal("missing selection fell back to a different task")
	}
}
func TestTaskStatusUsesActualStageAndDistinguishesSystemCheck(t *testing.T) {
	running := worker.LocalTaskView{Task: protocol.Task{State: "running"}, Progress: events.Progress{Stage: "downloading", CompletedBytes: 50, TotalBytes: 100}}
	if got := taskStatus(running); !strings.Contains(got, "50%") {
		t.Fatalf("running task lost actual progress: %s", got)
	}
	inspection := worker.LocalTaskView{Task: protocol.Task{State: "succeeded", Spec: protocol.TaskSpec{Type: protocol.InspectSystem}}}
	if got := taskStatus(inspection); strings.Contains(got, "安装") || !strings.Contains(got, "检查") {
		t.Fatalf("system check mislabeled installation: %s", got)
	}
}
