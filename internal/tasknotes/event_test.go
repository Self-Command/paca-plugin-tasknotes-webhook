package tasknotes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignatureRawBytes(t *testing.T) {
	raw := []byte(`{"a": 1}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))
	if !ValidSignature(raw, "secret", sig) || ValidSignature([]byte(`{"a":1}`), "secret", sig) || ValidSignature(raw, "wrong", sig) {
		t.Fatal("signature must authenticate exact bytes")
	}
}
func TestEnvelopeRejectsUnsafePathsAndOversizedFields(t *testing.T) {
	for _, value := range []string{"../outside.md", "/absolute.md", "C:/vault/task.md", "Tasks/\x00.md", strings.Repeat("x", 1025)} {
		e := Envelope{Event: "task.created", Timestamp: "2026-10-07T00:00:00Z"}
		e.Vault.Name = "paired"
		e.Data.Task = Task{Path: value, Title: "Task"}
		raw, _ := json.Marshal(e)
		if _, err := Decode(raw); err == nil {
			t.Fatalf("unsafe path accepted: %q", value)
		}
	}
	if !ValidPath("Tasks/中文任务.md") {
		t.Fatal("valid relative path rejected")
	}
}
func TestPrecisionAndDST(t *testing.T) {
	cases := []struct {
		s, zone, precision string
		bad                bool
	}{
		{"2026-10-07", "Asia/Shanghai", "day", false},
		{"2026-10-07T09:10", "Asia/Shanghai", "instant", false},
		{"2026-10-07T09:10:00.000", "Asia/Shanghai", "instant", false},
		{"2026-10-07T09:10:00+08:00", "Asia/Shanghai", "instant", false},
		{"2026-11-01T01:30", "America/New_York", "", true},
		{"2026-03-08T02:30", "America/New_York", "", true},
		{"", "Asia/Shanghai", "missing", false},
	}
	for _, c := range cases {
		got, err := ParseDate(c.s, c.zone)
		if (err != nil) != c.bad || (!c.bad && got.Precision != c.precision) {
			t.Fatalf("%+v: %+v %v", c, got, err)
		}
	}
}
func TestSQLDateDoesNotLoseInstantOrLocalCalendar(t *testing.T) {
	d, err := ParseDate("2026-10-07T00:10:00+08:00", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	core, day, err := CoreDate(d, "Asia/Shanghai")
	if err != nil || core != "2026-10-07T00:00:00Z" || day != "2026-10-07" || d.Value != "2026-10-06T16:10:00Z" {
		t.Fatal(core, day, d.Value, err)
	}
	if (Task{Recurrence: ""}).Recurring() || !(Task{Recurrence: "FREQ=DAILY"}).Recurring() {
		t.Fatal("empty recurrence is an ordinary task")
	}
}

func TestSnapshotIgnoresClockAndPathButKeepsManagedFields(t *testing.T) {
	original := Task{Path: "Tasks/one.md", ID: "old", Title: "one", Tags: []string{"b", "a"}, DateModified: "old"}
	moved := original
	moved.Path = "Archive/one.md"
	moved.ID = "new"
	moved.DateModified = "new"
	moved.Tags = []string{"a", "b"}
	if CanonicalHash(original, false) != CanonicalHash(moved, false) {
		t.Fatal("volatile fields changed snapshot")
	}
	moved.Priority = "high"
	if CanonicalHash(original, false) == CanonicalHash(moved, false) {
		t.Fatal("priority ignored")
	}
	e := Envelope{Event: "task.unarchived"}
	e.Data.Task.Archived = true
	if e.EffectiveTask().Archived {
		t.Fatal("stale cached archived flag overrode unarchive")
	}
}

func TestArchiveMoveIgnoresOnlyConfiguredTag(t *testing.T) {
 original:=Task{Path:"Tasks/one.md",Title:"one",DateCreated:"2026-10-07T00:00:00Z",Tags:[]string{"task","study"}}
 moved:=original;moved.Path="Archive/one.md";moved.Archived=true;moved.Tags=[]string{"task","study","custom-archive"}
 if !ArchiveEquivalent(original,moved,"custom-archive"){t.Fatal("official archive tag broke move association")}
 moved.DateCreated="2026-10-07T00:00:01Z"
 if ArchiveEquivalent(original,moved,"custom-archive"){t.Fatal("another task birth time merged")}
 moved=original;moved.Path="Archive/another.md"
 if ArchiveEquivalent(original,moved,"archived"){t.Fatal("another filename merged")}
}
