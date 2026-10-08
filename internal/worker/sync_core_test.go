package worker

import (
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"reflect"
	"testing"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
)

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
