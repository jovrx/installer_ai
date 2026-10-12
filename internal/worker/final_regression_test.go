package worker

import (
	"context"
	"fmt"
	"remoteops/pkg/protocol"
	"testing"
	"time"
)

func TestFinalReviewRunningTaskSurvivesBacklog(t *testing.T) {
	started := make(chan struct{}, 1)
	s := testServer(t, executorFunc(func(ctx context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, _ func(string, string)) (protocol.TaskResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return protocol.TaskResult{}, ctx.Err()
	}))
	defer func() { s.Close(); s.Wait() }()
	for i := 0; i < 101; i++ {
		d := dispatch(i == 0)
		d.Task.ID = fmt.Sprintf("review_%03d", i)
		d.Task.IdempotencyKey = fmt.Sprintf("review_key_%03d", i)
		d.Task.Spec.TimeoutSeconds = 120
		d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
		if i == 0 {
			d.ApprovalDigest = d.Task.Digest
		}
		if w := rpc(t, s, "POST", "/v1/tasks", d, true); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		if i == 0 {
			<-started
		}
	}
	views := s.LocalStatus()
	if len(views) != 100 || views[0].Task.ID != "review_100" || views[98].Task.ID != "review_002" {
		t.Fatalf("bounded recent history changed: %d tasks", len(views))
	}
	for i, v := range s.LocalStatus() {
		if v.Task.ID != views[i].Task.ID {
			t.Fatal("refresh changed stable task IDs")
		}
	}
	for _, v := range views {
		if v.Task.ID == "review_000" {
			if err := s.CancelLocalTask(v.Task.ID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				s.mu.Lock()
				state := s.tasks[v.Task.ID].task.State
				s.mu.Unlock()
				if state == protocol.Cancelled {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("retained running task was not cancelled")
			return
		}
	}
	t.Fatalf("active installation omitted from all %d returned tasks; newest=%s oldest=%s", len(views), views[0].Task.ID, views[len(views)-1].Task.ID)
}

func TestFinalFixRecentViewsCapsAndStableOrdering(t *testing.T) {
	tasks := []LocalTaskView{}
	for i := 0; i < 105; i++ {
		tasks = append(tasks, LocalTaskView{Task: protocol.Task{ID: fmt.Sprint(i), State: protocol.TaskRunning}})
	}
	for _, limit := range []int{0, 20, 100} {
		views := RecentTaskViews(tasks, limit)
		if len(views) != limit {
			t.Fatalf("cap %d got %d", limit, len(views))
		}
		for i, v := range views {
			if v.Task.ID != fmt.Sprint(i) {
				t.Fatalf("cap %d reordered %s", limit, v.Task.ID)
			}
		}
	}
	for i := range tasks {
		tasks[i].Task.State = protocol.Queued
	}
	tasks[104].Task.State = protocol.TaskRunning
	views := RecentTaskViews(tasks, 20)
	if len(views) != 20 || views[18].Task.ID != "18" || views[19].Task.ID != "104" {
		t.Fatalf("reserved active slot: %+v", views)
	}
}
