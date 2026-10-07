package worker

import (
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"testing"
	"time"
)

func TestLifecycleDoesNotNeedChangedModificationTime(t *testing.T) {
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	initial := tasknotes.Task{Path: "Tasks/one.md", Title: "one", Status: "open", DateModified: now.Add(-time.Hour).Format(time.RFC3339Nano)}
	base := tasknotes.Envelope{Event: "task.created", Timestamp: now.Format(time.RFC3339Nano)}
	base.Data.Task = initial
	for _, kind := range []string{"task.updated", "task.completed", "task.deleted", "task.archived", "task.unarchived"} {
		t.Run(kind, func(t *testing.T) {
			s := source{State: "linked", Snapshot: &initial, EventAt: &now, Hash: base.SnapshotHash()}
			e := base
			e.Event = kind
			if kind == "task.updated" {
				e.Data.Previous = &initial
				e.Data.Task.Priority = "high"
			}
			if kind == "task.completed" {
				e.Data.Task.Status = "done"
			}
			if kind == "task.unarchived" {
				archived := initial
				archived.Archived = true
				s.Snapshot = &archived
				prior := base
				prior.Event = "task.archived"
				s.Hash = prior.SnapshotHash()
			}
			decision, message := eventDecision(s, e, now)
			if decision != "apply" {
				t.Fatalf("%s: %s", decision, message)
			}
		})
	}
}
func TestAmbiguousTieAndStaleDeleteAreHeld(t *testing.T) {
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	task := tasknotes.Task{Title: "accepted", Status: "open"}
	s := source{State: "linked", Snapshot: &task, EventAt: &now, Hash: "accepted"}
	e := tasknotes.Envelope{Event: "task.updated", Timestamp: now.Format(time.RFC3339Nano)}
	e.Data.Task = task
	e.Data.Task.Title = "different"
	if decision, _ := eventDecision(s, e, now); decision != "conflict" {
		t.Fatal(decision)
	}
	e.Event = "task.deleted"
	e.Timestamp = now.Add(-time.Second).Format(time.RFC3339Nano)
	if decision, _ := eventDecision(s, e, now); decision != "stale" {
		t.Fatal(decision)
	}
	s.State = "deleted"
	e.Event = "task.updated"
	e.Timestamp = now.Add(time.Second).Format(time.RFC3339Nano)
	if decision, _ := eventDecision(s, e, now); decision != "conflict" {
		t.Fatal(decision)
	}
}
func TestMissedOrConcurrentPreviousDoesNotSilentlyOverwrite(t *testing.T) {
	now := time.Now()
	task := tasknotes.Task{Title: "server accepted", Status: "open"}
	prior := task
	prior.Title = "another device"
	e := tasknotes.Envelope{Event: "task.updated", Timestamp: now.Add(time.Second).Format(time.RFC3339Nano)}
	e.Data.Previous = &prior
	e.Data.Task = prior
	e.Data.Task.Priority = "high"
	s := source{State: "linked", Snapshot: &task, EventAt: &now}
	if decision, _ := eventDecision(s, e, now); decision != "conflict" {
		t.Fatal(decision)
	}
}
