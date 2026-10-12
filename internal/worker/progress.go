package worker

import (
	"encoding/json"
	"errors"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"sort"
	"time"
)

type LocalTaskView struct {
	Task        protocol.Task
	PackageName string
	Progress    events.Progress
	Logs        []protocol.LogEntry
}

// RecentTaskViews reserves bounded slots for executing tasks, then fills with
// the newest other tasks. It preserves the supplied recent-first ordering.
// When running tasks alone exceed limit, their newest limit entries are kept.
func RecentTaskViews(tasks []LocalTaskView, limit int) []LocalTaskView {
	limit = min(max(limit, 0), len(tasks))
	if limit == len(tasks) {
		return tasks
	}
	keep := make([]bool, len(tasks))
	n := 0
	for i, v := range tasks {
		if v.Task.State == protocol.TaskRunning && n < limit {
			keep[i] = true
			n++
		}
	}
	for i := range tasks {
		if !keep[i] && n < limit {
			keep[i] = true
			n++
		}
	}
	out := make([]LocalTaskView, 0, limit)
	for i, v := range tasks {
		if keep[i] {
			out = append(out, v)
		}
	}
	return out
}

// LocalStatus returns at most 100 tasks, reserving executing work before recent
// history. Newest-first ordering and stable ID ties are retained.
func (s *Server) LocalStatus() []LocalTaskView {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]*record, 0, len(s.tasks))
	for _, r := range s.tasks {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].task.CreatedAt.Equal(records[j].task.CreatedAt) {
			return records[i].task.ID > records[j].task.ID
		}
		return records[i].task.CreatedAt.After(records[j].task.CreatedAt)
	})
	views := make([]LocalTaskView, 0, len(records))
	for _, r := range records {
		name := ""
		if p := r.task.Spec.Package; p != nil {
			if entry, ok := protocol.FindPackage(s.opt.Catalog, p.ID, p.Version); ok {
				name = entry.Name
			}
		}
		views = append(views, LocalTaskView{Task: r.task, PackageName: name, Progress: r.progress, Logs: r.logs})
	}
	// Detach only the bounded result while holding the records' mutex.
	b, _ := json.Marshal(RecentTaskViews(views, 100))
	var out []LocalTaskView
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Server) CancelLocalTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.tasks[id]
	if r == nil {
		return errors.New("task not cached")
	}
	s.cancelRecord(r)
	return nil
}

// cancelRecord requires s.mu. Cancellation takes precedence over late progress.
func (s *Server) cancelRecord(r *record) {
	if !r.task.State.Terminal() {
		r.progress = events.Progress{Stage: "cancelling"}
		r.cancel()
	}
}

func (s *Server) EndReason() string { s.mu.Lock(); defer s.mu.Unlock(); return s.endReason }

func (s *Server) CloseWithReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	switch reason {
	case "expired", "heartbeat_lost", "controller_closed", "local_closed":
	default:
		reason = "local_closed"
	}
	s.endReason = reason
	s.closed = true
	s.stop()
	for _, r := range s.tasks {
		s.cancelRecord(r)
	}
}

// inactiveReason requires s.mu.
func (s *Server) inactiveReason() string {
	if s.closed {
		return s.endReason
	}
	if !time.Now().Before(s.opt.Session.ExpiresAt) {
		return "expired"
	}
	return "heartbeat_lost"
}
