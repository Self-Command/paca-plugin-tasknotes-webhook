package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"github.com/jackc/pgx/v5"
	"net/url"
	"sort"
	"strings"
	"time"
)

type syncConfig struct {
	connection
	Mode       string
	Reverse    map[string]string
	TaskTag    string
	Recurrence bool
}
type nativeTask struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description json.RawMessage `json:"description"`
	Status      string          `json:"status_id"`
	Importance  int             `json:"importance"`
	Start       *string         `json:"start_date"`
	Due         *string         `json:"due_date"`
	Tags        []string        `json:"tags"`
	Custom      map[string]any  `json:"custom_fields"`
}

func (w *Worker) syncConfig(ctx context.Context, id string) (syncConfig, error) {
	c := syncConfig{connection: connection{ID: id}}
	var sm, pm, reverse []byte
	err := w.Pool.QueryRow(ctx, "SELECT project_id::text,timezone,status_map,priority_map,revision,archive_tag,sync_mode,reverse_status_map,task_tag,recurrence_enabled FROM connections WHERE id=$1 AND enabled", id).Scan(&c.Project, &c.Timezone, &sm, &pm, &c.Revision, &c.ArchiveTag, &c.Mode, &reverse, &c.TaskTag, &c.Recurrence)
	if err != nil {
		return c, err
	}
	if json.Unmarshal(sm, &c.Statuses) != nil || json.Unmarshal(pm, &c.Priorities) != nil || json.Unmarshal(reverse, &c.Reverse) != nil {
		return c, errors.New("invalid sync mapping")
	}
	return c, nil
}
func datePart(value *string) string {
	if value == nil {
		return ""
	}
	if len(*value) >= 10 {
		return (*value)[:10]
	}
	return ""
}
func nativeSnapshot(t nativeTask, c syncConfig, previous tasksync.Snapshot) (tasksync.Snapshot, []string) {
	tags := sharedTags(t.Tags, c)
	out := tasksync.Snapshot{"title": t.Title, "tags": tags, "archived": false, "recurrence": nil}
	warnings := []string{}
	extra, _ := t.Custom["_task_sync_v1"].(map[string]any)
	var description any
	_ = json.Unmarshal(t.Description, &description)
	if original, ok := extra["details_source"].(string); ok && extra["details_core_hash"] == tasksync.Hash(description) {
		out["details"] = tasksync.TaskBody(original)
	} else if text, err := tasksync.Markdown(t.Description); err == nil {
		out["details"] = text
	} else {
		out["details"] = previous["details"]
		warnings = append(warnings, "任务内容包含暂不支持的格式，原始内容已保留。")
	}
	state := c.Reverse[t.Status]
	if t.Status == "" {
		state = c.Reverse["@none"]
	}
	if state == "" {
		keys := []string{}
		for key, id := range c.Statuses {
			if id == t.Status && !strings.HasPrefix(key, "@") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) == 1 {
			state = keys[0]
		}
		if old, _ := previous["status"].(string); c.Statuses[old] == t.Status {
			state = old
		}
	}
	if t.Status == c.Statuses["@archived"] && t.Status != "" {
		out["archived"] = true
		if old, _ := previous["status"].(string); old != "" {
			state = old
		}
	}
	if state == "" {
		warnings = append(warnings, "请配置此项目状态对应的 TaskNotes 状态。")
	}
	out["status"] = state
	priority := "normal"
	distance := int(^uint(0) >> 1)
	for name, value := range c.Priorities {
		d := value - t.Importance
		if d < 0 {
			d = -d
		}
		if d < distance || (d == distance && name < priority) {
			priority = name
			distance = d
		}
	}
	out["priority"] = priority
	if t.Importance == 0 {
		out["priority"] = "normal"
	}
	meta, _ := t.Custom["_integration_state_v1"].(map[string]any)
	for _, field := range []struct {
		name, prefix string
		date         *string
	}{{"scheduled", "start", t.Start}, {"due", "due", t.Due}} {
		day := datePart(field.date)
		out[field.name] = nil
		if day != "" {
			out[field.name] = day
		}
		if core, _ := meta[field.prefix+"_core_date"].(string); core == day && day != "" {
			if raw, _ := meta[field.prefix+"_source"].(string); raw != "" {
				out[field.name] = raw
			}
		}
	}
	for _, key := range []string{"recurrence", "recurrence_anchor", "complete_instances", "skipped_instances", "recurrence_parent", "occurrence_date"} {
		if value, ok := previous[key]; ok {
			out[key] = value
		}
	}
	if extra, ok := t.Custom["_task_sync_v1"].(map[string]any); ok {
		for _, key := range []string{"recurrence", "recurrence_anchor", "complete_instances", "skipped_instances", "recurrence_parent", "occurrence_date"} {
			if value, exists := extra[key]; exists {
				out[key] = value
			}
		}
	}
	return out, warnings
}
func (w *Worker) SyncTick(ctx context.Context) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	if err := w.applySyncOperation(ctx); err != nil {
		return err
	}
	rows, err := w.Pool.Query(ctx, "SELECT c.id::text FROM connections c LEFT JOIN sync_scan_state s ON s.connection_id=c.id LEFT JOIN sync_dirty d ON d.project_id=c.project_id WHERE c.enabled AND c.sync_mode<>'off' AND (s.last_complete IS NULL OR s.last_complete<NOW()-INTERVAL '1 minute' OR d.updated_at>s.last_complete) ORDER BY s.last_complete NULLS FIRST LIMIT 1")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = w.scanSync(ctx, id); err != nil {
			_, _ = w.Pool.Exec(ctx, "INSERT INTO sync_scan_state(connection_id,last_error) VALUES($1,$2) ON CONFLICT(connection_id) DO UPDATE SET last_error=EXCLUDED.last_error", id, "任务对账暂未完成，未执行缺失任务删除。")
			return err
		}
	}
	return nil
}
func (w *Worker) scanSync(ctx context.Context, id string) error {
	c, err := w.syncConfig(ctx, id)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	cursor := ""
	scanStart := time.Now()
	for page := 0; page < 10000; page++ {
		var list struct {
			Items []nativeTask `json:"items"`
			Next  *string      `json:"next_cursor"`
		}
		route := "/projects/" + c.Project + "/tasks?page_size=200"
		if cursor != "" {
			route += "&cursor=" + url.QueryEscape(cursor)
		}
		if err = w.call(ctx, "GET", route, nil, &list); err != nil {
			return err
		}
		for _, task := range list.Items {
			seen[task.ID] = true
			if err = w.acceptNative(ctx, c, task, false); err != nil {
				return err
			}
		}
		if list.Next == nil || *list.Next == "" {
			break
		}
		if *list.Next == cursor {
			return errors.New("task pagination did not advance")
		}
		cursor = *list.Next
		if page == 9999 {
			return errors.New("task pagination limit reached")
		}
	}
	rows, err := w.Pool.Query(ctx, "SELECT paca_task_id::text FROM sync_objects WHERE connection_id=$1 AND NOT deleted AND paca_task_id IS NOT NULL AND updated_at<$2", id, scanStart)
	if err != nil {
		return err
	}
	missing := []string{}
	for rows.Next() {
		var task string
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return err
		}
		if !seen[task] {
			missing = append(missing, task)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, task := range missing {
		var current nativeTask
		err = w.call(ctx, "GET", "/projects/"+c.Project+"/tasks/"+task, nil, &current)
		var api apiError
		if errors.As(err, &api) && api.Code == 404 {
			if err = w.acceptNative(ctx, c, nativeTask{ID: task}, true); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if err = w.acceptNative(ctx, c, current, false); err != nil {
				return err
			}
		}
	}
	_, err = w.Pool.Exec(ctx, "INSERT INTO sync_scan_state(connection_id,last_complete,last_error) VALUES($1,NOW(),'') ON CONFLICT(connection_id) DO UPDATE SET last_complete=NOW(),last_error=''", id)
	return err
}
func (w *Worker) acceptNative(ctx context.Context, c syncConfig, native nativeTask, deleted bool) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	identity := tasksync.ID(c.ID, native.ID)
	ref, _ := native.Custom["_integration_ref_v1"].(string)
	if ref == "" {
		ref = "sync:" + identity
	}
	var sourceID *int64
	var path, birth string
	var aliases []byte
	var previous []byte
	sourceErr := tx.QueryRow(ctx, "SELECT s.id,s.external_ref,COALESCE(s.snapshot->>'path',''),COALESCE(s.snapshot->>'dateCreated',''),s.snapshot,COALESCE((SELECT jsonb_agg(a.path) FROM path_aliases a WHERE a.source_id=s.id),'[]'::jsonb) FROM sources s WHERE s.connection_id=$1 AND s.paca_task_id=$2 AND s.state<>'superseded' ORDER BY s.id DESC LIMIT 1", c.ID, native.ID).Scan(&sourceID, &ref, &path, &birth, &previous, &aliases)
	if sourceErr != nil && !errors.Is(sourceErr, pgx.ErrNoRows) {
		return sourceErr
	}
	if aliases == nil {
		aliases = []byte("[]")
	}
	sourcePath, sourceBirth := path, birth
	_, err = tx.Exec(ctx, "INSERT INTO sync_objects(id,connection_id,paca_task_id,source_id,source_ref,path,note_created,path_aliases,binding_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9) ON CONFLICT(connection_id,paca_task_id) DO NOTHING", identity, c.ID, native.ID, sourceID, ref, path, birth, string(aliases), func() string {
		if path != "" {
			return "bound"
		}
		return "unbound"
	}())
	if err != nil {
		return err
	}
	var raw []byte
	var revision int64
	var wasDeleted bool
	var currentID string
	var priorWarning string
	err = tx.QueryRow(ctx, "SELECT id::text,snapshot,revision,deleted,path,note_created,source_ref,last_error FROM sync_objects WHERE connection_id=$1 AND paca_task_id=$2 FOR UPDATE", c.ID, native.ID).Scan(&currentID, &raw, &revision, &wasDeleted, &path, &birth, &ref, &priorWarning)
	if err != nil {
		return err
	}
	before := tasksync.Snapshot{}
	pathChanged := sourceID != nil && sourcePath != "" && (sourcePath != path || sourceBirth != birth)
	if pathChanged {
		path = sourcePath
		birth = sourceBirth
	}
	_ = json.Unmarshal(raw, &before)
	if len(before) == 0 && previous != nil {
		_ = json.Unmarshal(previous, &before)
	}
	snapshot, warnings := nativeSnapshot(native, c, before)
	if deleted {
		snapshot = before
		warnings = nil
	}
	if wasDeleted == deleted && !pathChanged && tasksync.Equal(snapshot, before) && priorWarning == strings.Join(warnings, " ") && len(raw) > 2 {
		return tx.Commit(ctx)
	}
	if len(raw) > 2 {
		revision++
	}
	nativeRaw, _ := json.Marshal(native)
	snapshotRaw, _ := json.Marshal(snapshot)
	kind := "task"
	if rule, ok := snapshot["recurrence"].(string); ok && rule != "" {
		kind = "series"
	}
	if date, ok := snapshot["occurrence_date"].(string); ok && date != "" {
		kind = "occurrence"
	}
	_, err = tx.Exec(ctx, "UPDATE sync_objects SET snapshot=$2::jsonb,paca_snapshot=$3::jsonb,revision=$4,deleted=$5,kind=$6,last_error=$7,path=$8,note_created=$9,source_id=COALESCE($10,source_id),path_aliases=$11::jsonb,updated_at=NOW() WHERE id=$1", currentID, string(snapshotRaw), string(nativeRaw), revision, deleted, kind, strings.Join(warnings, " "), path, birth, sourceID, string(aliases))
	if err != nil {
		return err
	}
	change := tasksync.Change{SyncID: currentID, TaskID: native.ID, Revision: revision, Kind: kind, Deleted: deleted, Path: path, Created: birth, SourceRef: ref, Snapshot: snapshot, Warnings: warnings}
	payload, _ := json.Marshal(change)
	_, err = tx.Exec(ctx, "INSERT INTO sync_changes(connection_id,object_id,revision,payload) VALUES($1,$2,$3,$4::jsonb)", c.ID, currentID, revision, string(payload))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func nativePatch(snapshot, changes tasksync.Snapshot, c syncConfig, current nativeTask, ref string) (map[string]any, error) {
	payload := map[string]any{}
	for key, value := range changes {
		switch key {
		case "title":
			title, ok := value.(string)
			if !ok || strings.TrimSpace(title) == "" || len([]rune(title)) > 500 {
				return nil, errors.New("任务标题不能为空或过长。")
			}
			payload["title"] = title
		case "details":
			text, _ := value.(string)
			payload["description"] = tasksync.Blocks(text)
		case "tags":
			payload["tags"] = value
		case "priority":
			name, _ := value.(string)
			importance, ok := c.Priorities[name]
			if !ok {
				return nil, errors.New("请配置任务优先级映射。")
			}
			payload["importance"] = importance
		case "status":
			name, _ := value.(string)
			id := c.Statuses[name]
			if id == "" {
				return nil, errors.New("请配置任务状态映射。")
			}
			payload["status_id"] = id
		}
	}
	meta, _ := current.Custom["_integration_state_v1"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["version"] = 2
	meta["source"] = "task-sync"
	meta["timezone"] = c.Timezone
	meta["archived"] = snapshot["archived"]
	rule, recurring := snapshot["recurrence"].(string)
	meta["recurring"] = recurring && rule != ""
	for _, f := range []struct{ field, prefix, native string }{{"scheduled", "start", "start_date"}, {"due", "due", "due_date"}} {
		if _, changed := changes[f.field]; changed {
			raw, _ := snapshot[f.field].(string)
			date, err := tasknotes.ParseDate(raw, c.Timezone)
			if err != nil {
				return nil, err
			}
			core, day, err := tasknotes.CoreDate(date, c.Timezone)
			if err != nil {
				return nil, err
			}
			if date.Precision == "day" {
				core = raw + "T00:00:00Z"
				day = raw
			}
			payload[f.native] = core
			meta[f.prefix+"_precision"] = date.Precision
			meta[f.prefix+"_instant"] = date.Value
			meta[f.prefix+"_source"] = raw
			meta[f.prefix+"_core_date"] = day
		}
	}
	if archived, _ := snapshot["archived"].(bool); archived {
		if c.Statuses["@archived"] == "" {
			return nil, errors.New("请配置归档状态映射。")
		}
		payload["status_id"] = c.Statuses["@archived"]
	} else if _, changed := changes["archived"]; changed {
		name, _ := snapshot["status"].(string)
		if c.Statuses[name] == "" {
			return nil, errors.New("请配置取消归档后的任务状态。")
		}
		payload["status_id"] = c.Statuses[name]
	}
	extra := map[string]any{}
	if old, ok := current.Custom["_task_sync_v1"].(map[string]any); ok {
		for key, value := range old {
			extra[key] = value
		}
	}
	if description, changed := payload["description"]; changed {
		extra["details_source"] = snapshot["details"]
		extra["details_core_hash"] = tasksync.Hash(description)
	}
	for _, key := range []string{"recurrence", "recurrence_anchor", "complete_instances", "skipped_instances", "recurrence_parent", "occurrence_date"} {
		extra[key] = snapshot[key]
	}
	custom := map[string]any{}
	for key, value := range current.Custom {
		custom[key] = value
	}
	custom["_integration_ref_v1"] = ref
	custom["_integration_state_v1"] = meta
	custom["_task_sync_v1"] = extra
	payload["custom_fields"] = custom
	return payload, nil
}
func stringError(err error) string {
	var api apiError
	if errors.As(err, &api) {
		return fmt.Sprintf("任务操作暂未完成，请稍后重试（%d）。", api.Code)
	}
	return "任务操作暂未确认，请稍后重试。"
}
