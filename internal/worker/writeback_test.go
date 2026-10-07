package worker

import (
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"testing"
	"time"
)

func TestReceiptCandidateDoesNotAuthorizeTitleOrTimeEdits(t *testing.T) {
	now := time.Now().UTC()
	details := "正文"
	base := tasknotes.Task{Path: "Tasks/a.md", Title: "读书", Status: "open", Scheduled: "2026-10-07T09:00:00+08:00", Details: &details}
	s := source{State: "linked", Snapshot: &base, EventAt: &now}
	e := tasknotes.Envelope{Event: "task.updated", Timestamp: now.Format(time.RFC3339Nano)}
	e.Data.Task = base
	e.Data.Task.Status = "done"
	fields, ok := echoFields(s, e)
	if !ok || len(fields) != 1 || fields[0] != "status" {
		t.Fatal("status echo rejected", fields, ok)
	}
	e.Data.Task.Title = "用户改标题"
	if _, ok = echoFields(s, e); ok {
		t.Fatal("title edit swallowed")
	}
	e.Data.Task = base
	e.Data.Task.Scheduled = "2026-10-07T10:00:00+08:00"
	if _, ok = echoFields(s, e); ok {
		t.Fatal("time edit swallowed")
	}
	e.Data.Task = base
	e.Timestamp = now.Add(-time.Second).Format(time.RFC3339Nano)
	if _, ok = echoFields(s, e); ok {
		t.Fatal("stale echo changed accepted snapshot")
	}
}
