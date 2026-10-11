package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
)

type nativeFieldChange struct {
	Field string `json:"field"`
	New   any    `json:"new"`
}
type nativeWrite struct {
	From     time.Time      `json:"write_from"`
	Until    time.Time      `json:"write_until"`
	Expected map[string]any `json:"write_expected"`
}

// Worker writeback carries its original server receipt order. Its delayed core
// event must never pretend to be a new edit made after a later queued request.
func ownNativeEvent(topic string, at time.Time, content []byte, writes []nativeWrite) bool {
	var event struct {
		Changes []nativeFieldChange `json:"changes"`
		Title   string              `json:"title"`
	}
	if json.Unmarshal(content, &event) != nil {
		return false
	}
	for _, write := range writes {
		if write.From.IsZero() || write.Until.IsZero() || at.Before(write.From) || at.After(write.Until) {
			continue
		}
		if topic == "task.created" && event.Title == write.Expected["title"] {
			return true
		}
		custom, match := false, true
		for _, change := range event.Changes {
			field := change.Field
			if field == "custom_fields" {
				custom = true
				continue
			}
			if field == "status" {
				field = "status_id"
			}
			expected, present := write.Expected[field]
			if !present {
				match = false
				break
			}
			if field == "status_id" {
				continue
			}
			if field == "start_date" || field == "due_date" {
				left, leftOK := expected.(string)
				right, rightOK := change.New.(string)
				if leftOK && rightOK && len(left) >= 10 && len(right) >= 10 && left[:10] == right[:10] {
					continue
				}
			}
			if !tasksync.Equal(expected, change.New) {
				match = false
				break
			}
		}
		if custom && match {
			return true
		}
	}
	return false
}

func sharedNativeFields(topic string, content []byte) map[string]bool {
	fields := map[string]bool{}
	if topic == "task.deleted" {
		fields["delete"] = true
		return fields
	}
	var event struct {
		Changes []nativeFieldChange `json:"changes"`
	}
	if json.Unmarshal(content, &event) != nil {
		return fields
	}
	for _, change := range event.Changes {
		switch change.Field {
		case "title", "tags":
			fields[change.Field] = true
		case "description":
			fields["details"] = true
		case "importance":
			fields["priority"] = true
		case "start_date":
			fields["scheduled"] = true
		case "due_date":
			fields["due"] = true
		case "status":
			fields["status"] = true
			fields["archived"] = true
		case "custom_fields":
			fields["custom"] = true
		}
	}
	return fields
}

func (w *Worker) laterNativeFields(ctx context.Context, id int64, task string, current nativeTask) (map[string]bool, error) {
	var received time.Time
	if err := w.Pool.QueryRow(ctx, "SELECT created_at FROM sync_operations WHERE id=$1", id).Scan(&received); err != nil {
		return nil, err
	}
	rows, err := w.Pool.Query(ctx, "SELECT result FROM sync_operations WHERE object_id=(SELECT object_id FROM sync_operations WHERE id=$1) AND id<$1 AND state='applied' AND result ? 'write_from'", id)
	if err != nil {
		return nil, err
	}
	writes := []nativeWrite{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var write nativeWrite
		if json.Unmarshal(raw, &write) == nil {
			writes = append(writes, write)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = w.Pool.Query(ctx, "SELECT topic,occurred_at,content FROM sync_native_events WHERE task_id=$1 AND occurred_at>$2 ORDER BY occurred_at", task, received)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	later := map[string]bool{}
	observedCurrent := !current.UpdatedAt.After(received)
	for rows.Next() {
		var topic string
		var at time.Time
		var raw []byte
		if err = rows.Scan(&topic, &at, &raw); err != nil {
			return nil, err
		}
		if !at.Before(current.UpdatedAt) {
			observedCurrent = true
		}
		if ownNativeEvent(topic, at, raw, writes) {
			continue
		}
		for field := range sharedNativeFields(topic, raw) {
			later[field] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !observedCurrent {
		var own bool
		if err = w.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sync_operations WHERE object_id=(SELECT object_id FROM sync_operations WHERE id=$1) AND id<$1 AND state='applied' AND NULLIF(result->>'native_updated_at','')::timestamptz=$2)", id, current.UpdatedAt).Scan(&own); err != nil {
			return nil, err
		}
		if !own {
			return nil, errors.New("waiting for native ordering event")
		}
	}
	if current.ID == "" {
		var deletedAt time.Time
		var recorded bool
		if err = w.Pool.QueryRow(ctx, "SELECT o.updated_at,EXISTS(SELECT 1 FROM sync_native_events e WHERE e.task_id=$2 AND e.topic='task.deleted') FROM sync_objects o WHERE o.id=(SELECT object_id FROM sync_operations WHERE id=$1)", id, task).Scan(&deletedAt, &recorded); err != nil {
			return nil, err
		}
		if deletedAt.After(received) && !recorded {
			return nil, errors.New("waiting for native deletion order")
		}
	}
	return later, nil
}
