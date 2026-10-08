// Package tasksync defines task synchronization without wall-clock overwrite rules.
package tasksync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

var Fields = map[string]bool{"title": true, "details": true, "scheduled": true, "due": true, "status": true, "priority": true, "tags": true, "archived": true, "recurrence": true, "recurrence_anchor": true, "complete_instances": true, "skipped_instances": true, "recurrence_parent": true, "occurrence_date": true}

type Snapshot map[string]any
type Operation struct {
	OpID         string   `json:"op_id"`
	SyncID       string   `json:"sync_id"`
	BaseRevision int64    `json:"base_revision"`
	Kind         string   `json:"kind"`
	Base         Snapshot `json:"base"`
	Changes      Snapshot `json:"changes"`
	Path         string   `json:"path,omitempty"`
	Created      string   `json:"note_created,omitempty"`
}
type Change struct {
	Cursor    int64    `json:"cursor"`
	SyncID    string   `json:"sync_id"`
	TaskID    string   `json:"task_id"`
	Revision  int64    `json:"revision"`
	Kind      string   `json:"kind"`
	Deleted   bool     `json:"deleted"`
	Path      string   `json:"path"`
	Created   string   `json:"note_created"`
	SourceRef string   `json:"source_ref"`
	Snapshot  Snapshot `json:"snapshot"`
	Warnings  []string `json:"warnings,omitempty"`
}

func Hash(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func ID(scope, identity string) string {
	b := sha256.Sum256([]byte(scope + "\n" + identity))
	b[6] = (b[6] & 15) | 80
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func Equal(a, b any) bool { return reflect.DeepEqual(normalize(a), normalize(b)) }
func normalize(v any) any { b, _ := json.Marshal(v); var n any; _ = json.Unmarshal(b, &n); return n }
func Diff(base, current Snapshot) Snapshot {
	out := Snapshot{}
	for field := range Fields {
		if !Equal(base[field], current[field]) {
			out[field] = current[field]
		}
	}
	return out
}

// Only explicit local changes participate. A newer unrelated remote field is preserved.
func Merge(base, changes, remote Snapshot) (Snapshot, []string, error) {
	merged := Snapshot{}
	for key, value := range remote {
		merged[key] = value
	}
	conflicts := []string{}
	for field, value := range changes {
		if !Fields[field] {
			return nil, nil, errors.New("unsupported task field")
		}
		if !Equal(remote[field], base[field]) && !Equal(remote[field], value) {
			conflicts = append(conflicts, field)
			continue
		}
		merged[field] = value
	}
	sort.Strings(conflicts)
	return merged, conflicts, nil
}
