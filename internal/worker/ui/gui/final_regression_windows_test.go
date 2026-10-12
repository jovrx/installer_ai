//go:build windows

package gui

import (
	"context"
	"fmt"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
	"testing"
	"time"
)

func reviewWindow(t *testing.T) *window {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	w, e := newWindow(client.New(context.Background(), client.Options{}))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(w.dispose)
	return w
}
func TestFinalReviewDefaultCurrentTaskSurvivesBacklog(t *testing.T) {
	w := reviewWindow(t)
	w.view = client.Snapshot{State: "online"}
	for i := 0; i < 20; i++ {
		w.view.Tasks = append(w.view.Tasks, worker.LocalTaskView{Task: protocol.Task{ID: fmt.Sprintf("queued_%02d", i), State: protocol.Queued}, Progress: events.Progress{Stage: "queued"}})
	}
	w.view.Tasks = append(w.view.Tasks, worker.LocalTaskView{Task: protocol.Task{ID: "running_old", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}})
	w.refresh()
	if w.selected != "running_old" {
		t.Fatalf("default current/cancel target=%s instead of running_old", w.selected)
	}
	if len(w.taskIDs) != 20 || w.taskIDs[19] != "running_old" {
		t.Fatalf("running task not in bounded selectable list: %v", w.taskIDs)
	}
	canCancel, _, _ := user32.NewProc("IsWindowEnabled").Call(w.controls[idCancel])
	if canCancel == 0 {
		t.Fatal("cancel control disabled for current task")
	}
	// Native list selection retains IDs through refresh and history expansion.
	sendMessage.Call(w.controls[idTasks], 0x186, 0, 0)
	w.command(idTasks)
	selected := w.selected
	w.refresh()
	if w.selected != selected {
		t.Fatal("refresh changed explicit selection")
	}
	w.command(idHistory)
	if w.selected != selected || len(w.taskIDs) != 21 {
		t.Fatal("history expansion changed selection")
	}
}
func TestFinalReviewStaticLongLogPreservesScroll(t *testing.T) {
	w := reviewWindow(t)
	w.view = client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "task", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}}}}
	w.logs = true
	for i := 0; i < 200; i++ {
		w.view.Tasks[0].Logs = append(w.view.Tasks[0].Logs, protocol.LogEntry{Timestamp: time.Now(), Message: fmt.Sprintf("log %03d %s", i, strings.Repeat("x", 80))})
	}
	w.refresh()
	up := int32(-10000)
	sendMessage.Call(w.controls[idLogs], 0xB6, 0, uintptr(up))
	sendMessage.Call(w.controls[idLogs], 0xB6, 0, 40)
	before, _, _ := sendMessage.Call(w.controls[idLogs], 0xCE, 0, 0)
	if before == 0 {
		t.Fatal("scroll setup failed")
	}
	w.refresh()
	after, _, _ := sendMessage.Call(w.controls[idLogs], 0xCE, 0, 0)
	if before != after {
		t.Fatalf("unchanged log refresh resets first visible line %d -> %d", before, after)
	}
	w.view.Tasks[0].Task.State = protocol.Succeeded
	w.refresh()
	before, _, _ = sendMessage.Call(w.controls[idLogs], 0xCE, 0, 0)
	w.refresh()
	after, _, _ = sendMessage.Call(w.controls[idLogs], 0xCE, 0, 0)
	if before != 40 || after != before {
		t.Fatalf("completed long log reading position: %d -> %d", before, after)
	}
	if text := textOf(w.controls[idLogs]); len(text) < 8192 || !strings.Contains(text, "log 199") {
		t.Fatal("control text remains truncated")
	}
}

func TestFinalFixAppendedLogPreservesReadingAndFollowsTail(t *testing.T) {
	w := reviewWindow(t)
	w.view = client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "task", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}}}}
	w.logs = true
	for i := 0; i < 200; i++ {
		w.view.Tasks[0].Logs = append(w.view.Tasks[0].Logs, protocol.LogEntry{Message: fmt.Sprintf("log %03d %s", i, strings.Repeat("中文", 40))})
	}
	w.refresh()
	h := w.controls[idLogs]
	// Move to old lines and select text there; both survive a changed status header.
	sendMessage.Call(h, 0xB1, 100, 120)
	up := int32(-10000)
	sendMessage.Call(h, 0xB6, 0, uintptr(up))
	sendMessage.Call(h, 0xB6, 0, 40)
	before, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	selection, _, _ := sendMessage.Call(h, 0xB0, 0, 0)
	if before == 0 {
		t.Fatal("scroll setup failed")
	}
	w.view.Tasks[0].Progress.Stage = "verifying_version"
	w.view.Tasks[0].Logs = append(w.view.Tasks[0].Logs, protocol.LogEntry{Message: "new output"})
	w.refresh()
	after, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	selAfter, _, _ := sendMessage.Call(h, 0xB0, 0, 0)
	if before != after || selection != selAfter {
		t.Fatalf("reading position changed: line %d -> %d selection %x -> %x", before, after, selection, selAfter)
	}
	// At the end with a collapsed caret, append follows the new last line.
	length, _, _ := getTextLength.Call(h)
	sendMessage.Call(h, 0xB1, length, length)
	sendMessage.Call(h, 0xB7, 0, 0)
	sendMessage.Call(h, 0xB6, 0, 10000)
	tailBefore, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	for i := 0; i < 30; i++ {
		w.view.Tasks[0].Logs = append(w.view.Tasks[0].Logs, protocol.LogEntry{Message: "tail append"})
	}
	w.refresh()
	tailAfter, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	if tailAfter <= tailBefore {
		t.Fatalf("tail did not follow: %d -> %d", tailBefore, tailAfter)
	}
	// Selection at the tail opts out of following appended output.
	sendMessage.Call(h, 0xB1, 100, 120)
	before, _, _ = sendMessage.Call(h, 0xCE, 0, 0)
	for i := 0; i < 30; i++ {
		w.view.Tasks[0].Logs = append(w.view.Tasks[0].Logs, protocol.LogEntry{Message: "selected tail append"})
	}
	w.refresh()
	after, _, _ = sendMessage.Call(h, 0xCE, 0, 0)
	if before != after {
		t.Fatalf("tail selection moved: %d -> %d", before, after)
	}
}
