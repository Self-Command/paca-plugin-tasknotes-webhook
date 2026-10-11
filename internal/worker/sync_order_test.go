package worker

import (
	"testing"
	"time"
)

func TestNativeWritebackKeepsOriginalReceiptOrder(t *testing.T) {
	now := time.Now().UTC()
	writes := []nativeWrite{{From: now.Add(-time.Second), Until: now.Add(time.Second), Expected: map[string]any{"title": "queued title", "custom_fields": true}}}
	if !ownNativeEvent("task.updated", now, []byte(`{"changes":[{"field":"title","new":"queued title"},{"field":"custom_fields"}]}`), writes) {
		t.Fatal("own queue write was counted as a new server edit")
	}
	for _, event := range []string{`{"changes":[{"field":"title","new":"later server title"}]}`, `{"changes":[{"field":"title","new":"later server title"},{"field":"custom_fields"}]}`, `{"changes":[{"field":"title","new":"queued title"}]}`} {
		if ownNativeEvent("task.updated", now, []byte(event), writes) {
			t.Fatal("a separate server edit was mistaken for queue writeback")
		}
	}
	if ownNativeEvent("task.updated", now.Add(2*time.Second), []byte(`{"changes":[{"field":"title","new":"queued title"},{"field":"custom_fields"}]}`), writes) {
		t.Fatal("write provenance outlived the actual request")
	}
}

func TestNativeOrderUsesOnlyChangedSharedFields(t *testing.T) {
	fields := sharedNativeFields("task.updated", []byte(`{"changes":[{"field":"title"},{"field":"tags"},{"field":"assignee"}]}`))
	if len(fields) != 2 || !fields["title"] || !fields["tags"] {
		t.Fatalf("unrelated fields became conflicts: %#v", fields)
	}
	if !sharedNativeFields("task.deleted", []byte(`{}`))["delete"] {
		t.Fatal("delete order disappeared")
	}
}
