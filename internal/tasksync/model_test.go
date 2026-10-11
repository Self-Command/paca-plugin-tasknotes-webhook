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

func TestPreferredMergeKeepsUnrelatedEditsInBothDirections(t *testing.T) {
	base := Snapshot{"title": "original", "priority": "normal", "tags": []string{"original"}}
	remote := Snapshot{"title": "server", "priority": "normal", "tags": []string{"server"}}
	changes := Snapshot{"title": "local", "priority": "high"}
	for _, prefer := range []string{"server", "local"} {
		merged, patch, conflicts, err := PreferredMerge(base, changes, remote, prefer)
		expected := "server"
		if prefer == "local" { expected = "local" }
		if err != nil || len(conflicts) != 0 || merged["title"] != expected || merged["priority"] != "high" || !Equal(merged["tags"], remote["tags"]) {
			t.Fatalf("%s discarded an unrelated edit: %#v %#v %v", prefer, merged, conflicts, err)
		}
		_, patched := patch["title"]
		if patched != (prefer == "local") { t.Fatalf("%s produced the wrong native patch: %#v", prefer, patch) }
	}
	if changes["title"] != "local" || remote["title"] != "server" { t.Fatal("input snapshots were changed") }
}

func TestPreferredMergeRejectsUnknownPriority(t *testing.T) {
	if _, _, _, err := PreferredMerge(Snapshot{}, Snapshot{}, Snapshot{}, "newest"); err == nil { t.Fatal("unknown priority accepted") }
}

func TestTaskBodyNormalizesDesktopAndMobileLineEndings(t *testing.T) {
	if TaskBody("标题\r\n正文\r\n") != TaskBody("标题\n正文\n") { t.Fatal("line endings created a content conflict") }
}
