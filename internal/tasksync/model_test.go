package tasksync

import "testing"

func TestExplicitFieldsMergeAgainstCommonSnapshot(t *testing.T) {
	base := Snapshot{"title": "old", "scheduled": "2026-10-08T09:00:00+08:00", "priority": "normal"}
	remote := Snapshot{"title": "AI title", "scheduled": base["scheduled"], "priority": "normal"}
	merged, conflicts, err := Merge(base, Snapshot{"priority": "high"}, remote)
	if err != nil || len(conflicts) != 0 || merged["title"] != "AI title" || merged["priority"] != "high" {
		t.Fatalf("merge lost remote edits: %#v %#v %v", merged, conflicts, err)
	}
}
func TestSameFieldPreservesBothVersionsAsConflict(t *testing.T) {
	base := Snapshot{"title": "old"}
	remote := Snapshot{"title": "remote"}
	merged, conflicts, err := Merge(base, Snapshot{"title": "local"}, remote)
	if err != nil || len(conflicts) != 1 || conflicts[0] != "title" || merged["title"] != "remote" {
		t.Fatalf("same field silently overwritten: %#v %#v", merged, conflicts)
	}
}
func TestManualTimeCannotRideAlongPriorityChange(t *testing.T) {
	base := Snapshot{"scheduled": "2026-10-08T09:00:00+08:00", "priority": "normal"}
	merged, conflicts, err := Merge(base, Snapshot{"priority": "high"}, base)
	if err != nil || len(conflicts) > 0 || merged["scheduled"] != base["scheduled"] {
		t.Fatal("unsubmitted date changed")
	}
}
func TestStableIDsIgnoreTaskTitleAndRequestTime(t *testing.T) {
	if ID("project", "task-id") != ID("project", "task-id") || ID("project", "task-id") == ID("other", "task-id") {
		t.Fatal("identity is not stable or scoped")
	}
}
