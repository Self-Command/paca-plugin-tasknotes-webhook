package worker

import (
	"encoding/json"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"reflect"
	"testing"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
)

func TestMarkdownSourceSurvivesUnrelatedNativeEdits(t *testing.T) {
	c := syncConfig{connection: connection{Statuses: map[string]string{"open": "todo"}, Priorities: map[string]int{"normal": 35}}}
	body := "| 字段 | 内容 |\n| --- | --- |\n| 链接 | [文档](https://example.org) |\n\n私人格式保持原样"
	snapshot := tasksync.Snapshot{"title": "内容往返", "status": "open", "priority": "normal", "details": body}
	patch, err := nativePatch(snapshot, snapshot, c, nativeTask{}, "source")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(patch["description"])
	native := nativeTask{Status: "todo", Title: "Paca 修改标题", Description: raw, Custom: patch["custom_fields"].(map[string]any)}
	got, warnings := nativeSnapshot(native, c, snapshot)
	if len(warnings) != 0 || got["details"] != body {
		t.Fatalf("original Markdown lost: %#v %v", got, warnings)
	}
	native.Description = json.RawMessage(`[{"type":"paragraph","content":[{"type":"text","text":"Paca 修改正文","styles":{}}],"children":[]}]`)
	got, warnings = nativeSnapshot(native, c, snapshot)
	if len(warnings) != 0 || got["details"] != "Paca 修改正文" {
		t.Fatalf("native edit masked by stale Markdown: %#v %v", got, warnings)
	}
}

func TestSystemTagsDoNotCauseCrossApplicationConflicts(t *testing.T) {
	c := syncConfig{connection: connection{ArchiveTag: "已归档"}, TaskTag: "任务"}
	s := sourceSnapshot(tasknotes.Task{Tags: []string{"#任务", "学习", "已归档", "学习"}}, c)
	if !reflect.DeepEqual(s["tags"], []string{"学习"}) {
		t.Fatalf("system tags leaked: %#v", s)
	}
	if !reflect.DeepEqual(sharedTags(nil, c), []string{}) {
		t.Fatal("empty tags must serialize as an array")
	}
}

func TestArchivedSnapshotKeepsUnderlyingConfiguredStatus(t *testing.T) {
	c := syncConfig{connection: connection{Statuses: map[string]string{"open": "todo", "done": "done", "@archived": "archive"}, Priorities: map[string]int{"normal": 35}}}
	snapshot, warnings := nativeSnapshot(nativeTask{ID: "task", Status: "archive"}, c, tasksync.Snapshot{"status": "open"})
	if snapshot["status"] != "open" || snapshot["archived"] != true || len(warnings) != 0 {
		t.Fatalf("archived snapshot lost state: %#v %v", snapshot, warnings)
	}
	snapshot["archived"] = false
	patch, err := nativePatch(snapshot, tasksync.Snapshot{"archived": false}, c, nativeTask{}, "sync:task")
	if err != nil || patch["status_id"] != "todo" {
		t.Fatalf("unarchive did not restore configured state: %#v %v", patch, err)
	}
}

func TestUnspecifiedNativeStatusRequiresExplicitDefault(t *testing.T) {
	c := syncConfig{connection: connection{Statuses: map[string]string{"open": "todo"}, Priorities: map[string]int{"normal": 35}}}
	_, warnings := nativeSnapshot(nativeTask{ID: "task"}, c, tasksync.Snapshot{})
	if len(warnings) == 0 {
		t.Fatal("unconfigured native status was guessed")
	}
	c.Reverse = map[string]string{"@none": "open"}
	snapshot, warnings := nativeSnapshot(nativeTask{ID: "task"}, c, tasksync.Snapshot{})
	if len(warnings) != 0 || snapshot["status"] != "open" || snapshot["priority"] != "normal" {
		t.Fatalf("explicit native defaults not honored: %#v %v", snapshot, warnings)
	}
}

func TestStoppedSeriesParentRemainsUnscheduled(t *testing.T) {
	c := syncConfig{connection: connection{Timezone: "Asia/Shanghai"}}
	parent := nativeTask{Custom: map[string]any{"_integration_state_v1": map[string]any{"recurring": true}}}
	patch, err := nativePatch(tasksync.Snapshot{"recurrence": nil}, tasksync.Snapshot{"recurrence": nil}, c, parent, "series")
	if err != nil {
		t.Fatal(err)
	}
	meta := patch["custom_fields"].(map[string]any)["_integration_state_v1"].(map[string]any)
	if meta["recurring"] != true || meta["series_parent"] != true {
		t.Fatal("stopping a series made its parent eligible for reminders")
	}
	period, err := nativePatch(tasksync.Snapshot{"recurrence": nil, "occurrence_date": "2026-10-09"}, tasksync.Snapshot{}, c, nativeTask{}, "period")
	if err != nil || period["custom_fields"].(map[string]any)["_integration_state_v1"].(map[string]any)["recurring"] != false {
		t.Fatal("an individual period was suppressed as a parent")
	}
}

func TestNativePatchKeepsTaskTimezoneOnUnrelatedEdits(t *testing.T) {
	c := syncConfig{connection:connection{Timezone:"Asia/Shanghai"}}
	current := nativeTask{Custom: map[string]any{"_integration_state_v1": map[string]any{"timezone": "America/New_York", "reminder_start_minutes": 20}}}
	patch, err := nativePatch(tasksync.Snapshot{"title": "标题"}, tasksync.Snapshot{"title": "标题"}, c, current, "sync:task")
	if err != nil {
		t.Fatal(err)
	}
	meta := patch["custom_fields"].(map[string]any)["_integration_state_v1"].(map[string]any)
	if meta["timezone"] != "America/New_York" || meta["reminder_start_minutes"] != 20 {
		t.Fatal("unrelated edit changed shared time", meta)
	}
}
