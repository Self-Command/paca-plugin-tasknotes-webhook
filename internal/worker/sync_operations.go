package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func rawTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (w *Worker) submitSyncOperation(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	if a.Mode != "enabled" {
		syncFail(out, 409, "请先启用双向任务同步。")
		return
	}
	var body tasksync.Operation
	if !syncRead(out, r, &body) {
		return
	}
	if !syncUUID.MatchString(body.SyncID) || len(body.OpID) < 8 || len(body.OpID) > 128 || (body.Kind != "create" && body.Kind != "update" && body.Kind != "delete") || body.BaseRevision < 0 {
		syncFail(out, 400, "任务操作标识无效。")
		return
	}
	if _, _, err = tasksync.Merge(body.Base, body.Changes, body.Base); err != nil {
		syncFail(out, 400, "包含未支持的任务字段。")
		return
	}
	raw, _ := json.Marshal(body)
	tx, err := w.Pool.Begin(r.Context())
	if err != nil {
		syncFail(out, 503, "任务操作暂时无法保存。")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,7))", a.Connection+":"+body.OpID); err != nil {
		syncFail(out, 503, "任务操作暂时无法保存。")
		return
	}
	var storedHash, state string
	var result []byte
	err = tx.QueryRow(r.Context(), "SELECT body_hash,state,result FROM sync_operations WHERE connection_id=$1 AND op_id=$2", a.Connection, body.OpID).Scan(&storedHash, &state, &result)
	if err == nil {
		if storedHash != tasksync.Hash(body) {
			syncFail(out, 409, "相同操作标识不能提交不同内容。")
			return
		}
		var value any
		_ = json.Unmarshal(result, &value)
		syncJSON(out, 200, map[string]any{"state": state, "result": value})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		syncFail(out, 503, "任务操作暂不可用。")
		return
	}
	if body.Kind == "create" {
		if body.BaseRevision != 0 {
			syncFail(out, 400, "新任务基础版本应为零。")
			return
		}
		_, err = tx.Exec(r.Context(), "INSERT INTO sync_objects(id,connection_id,source_ref,snapshot,paca_snapshot) VALUES($1,$2,$3,'{}','{}') ON CONFLICT(id) DO NOTHING", body.SyncID, a.Connection, "sync:"+body.SyncID)
		if err != nil {
			syncFail(out, 409, "任务标识已关联其他来源。")
			return
		}
	}
	var scope string
	err = tx.QueryRow(r.Context(), "SELECT connection_id::text FROM sync_objects WHERE id=$1", body.SyncID).Scan(&scope)
	if err != nil || scope != a.Connection {
		syncFail(out, 404, "任务未关联当前笔记库。")
		return
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,$3,$4,$5,$6::jsonb)", a.Connection, body.SyncID, a.Device, body.OpID, tasksync.Hash(body), string(raw))
	if err != nil || tx.Commit(r.Context()) != nil {
		syncFail(out, 503, "任务操作暂时无法保存。")
		return
	}
	syncJSON(out, 202, map[string]any{"state": "pending", "sync_id": body.SyncID})
}
func (w *Worker) syncOperationStatus(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var state, message string
	var raw []byte
	err = w.Pool.QueryRow(r.Context(), "SELECT state,error,result FROM sync_operations WHERE connection_id=$1 AND op_id=$2", a.Connection, r.PathValue("op")).Scan(&state, &message, &raw)
	if err != nil {
		syncFail(out, 404, "操作未找到。")
		return
	}
	var result any
	_ = json.Unmarshal(raw, &result)
	syncJSON(out, 200, map[string]any{"state": state, "error": message, "result": result})
}
func (w *Worker) applySyncOperation(ctx context.Context) error {
	var id int64
	var connectionID, state string
	var raw []byte
	err := w.Pool.QueryRow(ctx, "UPDATE sync_operations SET lease_until=NOW()+INTERVAL '90 seconds' WHERE id=(SELECT o.id FROM sync_operations o JOIN connections c ON c.id=o.connection_id WHERE c.enabled AND c.sync_mode='enabled' AND o.state IN('pending','retry','sending','waiting_period') AND o.next_attempt<=NOW() AND (o.lease_until IS NULL OR o.lease_until<NOW()) AND NOT EXISTS(SELECT 1 FROM sync_operations earlier WHERE earlier.object_id=o.object_id AND earlier.id<o.id AND earlier.state IN('pending','retry','sending','conflict','uncertain')) ORDER BY o.id LIMIT 1 FOR UPDATE OF o SKIP LOCKED) RETURNING id,connection_id::text,state,body").Scan(&id, &connectionID, &state, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		_, _ = w.Pool.Exec(context.Background(), "UPDATE sync_operations SET lease_until=NULL WHERE id=$1", id)
	}()
	c, err := w.syncConfig(ctx, connectionID)
	if err != nil {
		return err
	}
	var op tasksync.Operation
	if json.Unmarshal(raw, &op) != nil {
		return errors.New("invalid durable operation")
	}
	// Serializes HTTP operations per task, without assuming a transaction with Paca.
	conn, err := w.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", c.ID).Scan(&locked)
	if err != nil || !locked {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1,0))", c.ID)
	// A cleanup may have superseded the operation after it was claimed and before
	// this connection lock was acquired. Never apply its cached request afterward.
	if err = conn.QueryRow(ctx, "SELECT state FROM sync_operations WHERE id=$1", id).Scan(&state); err != nil {
		return err
	}
	if state != "pending" && state != "retry" && state != "sending" && state != "waiting_period" {
		return nil
	}
	var canonical *string
	if err = w.Pool.QueryRow(ctx, "SELECT canonical_id::text FROM sync_objects WHERE id=$1 AND connection_id=$2", op.SyncID, c.ID).Scan(&canonical); err != nil {
		return err
	}
	if canonical != nil {
		op.SyncID = *canonical
	}
	if op.Kind == "create" {
		parent, _ := op.Changes["recurrence_parent"].(string)
		date, _ := op.Changes["occurrence_date"].(string)
		if syncUUID.MatchString(parent) && date != "" {
			if ensureErr := w.ensureRequestedPeriod(ctx, c, parent, date); ensureErr != nil {
				return w.waitForPeriod(ctx, id)
			}
			var periodID string
			if lookupErr := w.Pool.QueryRow(ctx, "SELECT p.object_id::text FROM recurring_periods p JOIN recurring_series s ON s.object_id=p.series_id WHERE p.series_id=$1 AND p.occurrence_date=$2 AND s.connection_id=$3 AND p.state NOT IN('deleted','cancelled','skipped')", parent, date, c.ID).Scan(&periodID); lookupErr != nil {
				return w.waitForPeriod(ctx, id)
			}
			if periodID != op.SyncID {
				if _, err = w.Pool.Exec(ctx, "UPDATE sync_objects SET canonical_id=$2 WHERE id=$1", op.SyncID, periodID); err != nil {
					return err
				}
				op.SyncID = periodID
			}
			if op.OpID != "period-create:"+periodID {
				var expected []byte
				var ready bool
				if err = w.Pool.QueryRow(ctx, "SELECT p.expected,o.paca_task_id IS NOT NULL FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.object_id=$1", periodID).Scan(&expected, &ready); err != nil {
					return err
				}
				if !ready {
					// The canonical create must be able to pass this dependency, even
					// when the first local note uses the same stable period ID.
					return w.waitForPeriod(ctx, id)
				}
				// A first local materialization is based on the official period,
				// not an empty unrelated task. Concurrent Paca edits still conflict.
				if err = json.Unmarshal(expected, &op.Base); err != nil {
					return err
				}
			}
		}
	}
	var task, ref string
	var revision int64
	var beforeRaw []byte
	var deleted bool
	err = w.Pool.QueryRow(ctx, "SELECT COALESCE(paca_task_id::text,''),source_ref,revision,snapshot,deleted FROM sync_objects WHERE id=$1 AND connection_id=$2", op.SyncID, c.ID).Scan(&task, &ref, &revision, &beforeRaw, &deleted)
	if err != nil {
		return err
	}
	before := tasksync.Snapshot{}
	_ = json.Unmarshal(beforeRaw, &before)
	if op.Kind == "create" && task == "" && op.Path != "" && op.Created != "" {
		var existing string
		lookup := w.Pool.QueryRow(ctx, "SELECT paca_task_id::text FROM sources WHERE connection_id=$1 AND snapshot->>'path'=$2 AND snapshot->>'dateCreated'=$3 AND state='linked' AND paca_task_id IS NOT NULL ORDER BY id DESC LIMIT 1", c.ID, op.Path, op.Created).Scan(&existing)
		if lookup == nil {
			var native nativeTask
			if err = w.call(ctx, "GET", "/projects/"+c.Project+"/tasks/"+existing, nil, &native); err != nil {
				return w.syncOpRetry(ctx, id, err)
			}
			if err = w.acceptNative(ctx, c, native, false); err != nil {
				return err
			}
			var associated string
			if err = w.Pool.QueryRow(ctx, "SELECT id::text FROM sync_objects WHERE connection_id=$1 AND paca_task_id=$2", c.ID, existing).Scan(&associated); err != nil {
				return err
			}
			if _, err = w.Pool.Exec(ctx, "UPDATE sync_objects SET canonical_id=$2 WHERE id=$1", op.SyncID, associated); err != nil {
				return err
			}
			return w.syncOpDone(ctx, id, "superseded", map[string]any{"sync_id": associated, "task_id": existing}, "")
		} else if !errors.Is(lookup, pgx.ErrNoRows) {
			return lookup
		}
	}
	root := "/projects/" + c.Project + "/tasks"
	var current nativeTask
	if task != "" {
		err = w.call(ctx, "GET", root+"/"+task, nil, &current)
		var api apiError
		if err != nil && !((op.Kind == "delete" || deleted) && errors.As(err, &api) && api.Code == 404) {
			return w.syncOpRetry(ctx, id, err)
		}
		if current.ID != "" {
			if err = w.acceptNative(ctx, c, current, false); err != nil {
				return err
			}
			err = w.Pool.QueryRow(ctx, "SELECT revision,snapshot FROM sync_objects WHERE id=$1", op.SyncID).Scan(&revision, &beforeRaw)
			if err != nil {
				return err
			}
			_ = json.Unmarshal(beforeRaw, &before)
		}
	}
	merged, conflicts, err := tasksync.Merge(op.Base, op.Changes, before)
	if err != nil {
		return w.syncOpDone(ctx, id, "failed", map[string]any{}, "任务字段无效。")
	}
	if op.Kind == "create" && task == "" {
		merged = op.Changes
		conflicts = nil
	} else if op.BaseRevision > revision {
		return w.syncOpDone(ctx, id, "failed", map[string]any{}, "基础版本尚未存在。")
	}
	if op.Kind == "delete" && (len(tasksync.Diff(op.Base, before)) != 0 || op.BaseRevision != revision) {
		conflicts = []string{"delete"}
	}
	if deleted && op.Kind != "delete" {
		conflicts = []string{"delete"}
	}
	if len(conflicts) > 0 {
		conflictID := tasksync.ID(c.ID, "conflict:"+op.OpID)
		base, _ := json.Marshal(op.Base)
		local, _ := json.Marshal(op.Changes)
		remote, _ := json.Marshal(before)
		fields, _ := json.Marshal(conflicts)
		_, err = w.Pool.Exec(ctx, "INSERT INTO sync_conflicts(id,connection_id,object_id,operation_id,base_revision,base,local,remote,fields) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb) ON CONFLICT(id) DO NOTHING", conflictID, c.ID, op.SyncID, id, op.BaseRevision, string(base), string(local), string(remote), string(fields))
		if err != nil {
			return err
		}
		return w.syncOpDone(ctx, id, "conflict", map[string]any{"conflict_id": conflictID}, "两端存在不同修改，请选择保留的内容。")
	}
	if op.Kind == "delete" {
		if current.ID != "" {
			if err = w.call(ctx, "DELETE", root+"/"+task, nil, nil); err != nil {
				return w.syncOpRetry(ctx, id, err)
			}
		}
		if err = w.acceptNative(ctx, c, nativeTask{ID: task}, true); err != nil {
			return err
		}
		return w.syncOpDone(ctx, id, "applied", map[string]any{"sync_id": op.SyncID}, "")
	}
	patchChanges := op.Changes
	if op.Kind == "create" {
		patchChanges = merged
	}
	if rule, ok := merged["recurrence"].(string); ok && rule != "" {
		loc, loadErr := time.LoadLocation(c.Timezone)
		if loadErr != nil {
			return w.syncOpDone(ctx, id, "failed", map[string]any{}, "任务时区无效。")
		}
		now := time.Now().In(loc)
		if _, modelErr := recurrenceModel(ctx, c.Timezone, map[string]any{"task": merged, "today": now.Format("2006-01-02"), "end": now.Format("2006-01-02"), "series_id": op.SyncID}); modelErr != nil {
			return w.syncOpDone(ctx, id, "failed", map[string]any{}, modelErr.Error())
		}
	}
	if err = w.guardScheduleChange(ctx, c, task, before, merged); err != nil {
		if errors.Is(err, errScheduleFrozen) {
			return w.syncOpDone(ctx, id, "conflict", map[string]any{"status_code": 409, "task_id": task}, err.Error())
		}
		return w.syncOpRetry(ctx, id, err)
	}
	payload, err := nativePatch(merged, patchChanges, c, current, ref)
	if err != nil {
		return w.syncOpDone(ctx, id, "failed", map[string]any{}, err.Error())
	}
	if task == "" {
		if state == "sending" {
			found, findErr := w.findRef(ctx, c.Project, ref)
			if findErr != nil {
				return w.syncOpRetry(ctx, id, findErr)
			}
			if len(found) != 1 {
				return w.syncOpDone(ctx, id, "uncertain", map[string]any{}, "创建结果尚未确认，请核对后关联任务。")
			} else {
				task = found[0].ID
			}
		}
		if task == "" {
			if _, err = w.Pool.Exec(ctx, "UPDATE sync_operations SET state='sending' WHERE id=$1", id); err != nil {
				return err
			}
			err = w.call(ctx, "POST", root, payload, &current)
			if err != nil || current.ID == "" {
				return w.syncOpRetry(ctx, id, errors.New("create response unknown"))
			}
			task = current.ID
		}
		if _, err = w.Pool.Exec(ctx, "UPDATE sync_objects SET paca_task_id=$2,path=$3,note_created=$4 WHERE id=$1", op.SyncID, task, op.Path, op.Created); err != nil {
			return err
		}
	} else {
		if err = w.call(ctx, "PATCH", root+"/"+task, payload, &current); err != nil {
			return w.syncOpRetry(ctx, id, err)
		}
	}
	if current.ID == "" {
		if err = w.call(ctx, "GET", root+"/"+task, nil, &current); err != nil {
			return w.syncOpRetry(ctx, id, err)
		}
	}
	if err = w.acceptNative(ctx, c, current, false); err != nil {
		return err
	}
	observed, _ := nativeSnapshot(current, c, merged)
	_, verifyConflicts, _ := tasksync.Merge(merged, op.Changes, observed)
	if len(verifyConflicts) > 0 {
		return w.syncOpDone(ctx, id, "uncertain", map[string]any{}, "任务在写入期间发生变化，请核对。")
	}
	return w.syncOpDone(ctx, id, "applied", map[string]any{"sync_id": op.SyncID, "task_id": task}, "")
}

// A missing parent is a bounded dependency, not an infinite write retry.
func (w *Worker) waitForPeriod(ctx context.Context, id int64) error {
	_, err := w.Pool.Exec(ctx, "UPDATE sync_operations SET state=CASE WHEN attempts>=9 OR created_at<=clock_timestamp()-INTERVAL '30 minutes' THEN 'conflict' ELSE 'waiting_period' END,attempts=attempts+1,next_attempt=NOW()+LEAST(300,POWER(2,LEAST(attempts+1,8)))*INTERVAL '1 second',error='待关联：正在等待循环母任务或本期任务，请确认关联后重处理。' WHERE id=$1", id)
	return err
}
func (w *Worker) syncOpRetry(ctx context.Context, id int64, problem error) error {
	_, err := w.Pool.Exec(ctx, "UPDATE sync_operations SET state=CASE WHEN state='sending' THEN state ELSE 'retry' END,attempts=attempts+1,error=$2,next_attempt=NOW()+LEAST(300,POWER(2,LEAST(attempts+1,8))) * INTERVAL '1 second' WHERE id=$1", id, stringError(problem))
	return err
}
func (w *Worker) syncOpDone(ctx context.Context, id int64, state string, result any, message string) error {
	raw, _ := json.Marshal(result)
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "UPDATE sync_operations SET state=$2,result=$3::jsonb,error=$4 WHERE id=$1", id, state, string(raw), message)
	if err != nil {
		return err
	}
	var inbox *int64
	var bodyRaw []byte
	if err = tx.QueryRow(ctx, "SELECT inbox_id,body FROM sync_operations WHERE id=$1", id).Scan(&inbox, &bodyRaw); err != nil {
		return err
	}
	if inbox != nil {
		inboxState := state
		if state == "superseded" {
			inboxState = "applied"
		}
		_, err = tx.Exec(ctx, "UPDATE inbox SET state=$2,error=$3,updated_at=NOW() WHERE id=$1", *inbox, inboxState, message)
		if err != nil {
			return err
		}
		if state == "applied" {
			var op tasksync.Operation
			_ = json.Unmarshal(bodyRaw, &op)
			var eventRaw []byte
			if err = tx.QueryRow(ctx, "SELECT body FROM inbox WHERE id=$1", *inbox).Scan(&eventRaw); err != nil {
				return err
			}
			var envelope struct {
				Data struct {
					Task map[string]any `json:"task"`
				} `json:"data"`
			}
			_ = json.Unmarshal(eventRaw, &envelope)
			sourceRaw, _ := json.Marshal(envelope.Data.Task)
			nextState := "linked"
			if op.Kind == "delete" {
				nextState = "deleted"
			}
			_, err = tx.Exec(ctx, "UPDATE sources s SET snapshot=$2::jsonb,state=$3,updated_at=NOW() FROM sync_objects o WHERE o.id=$1 AND o.source_id=s.id", op.SyncID, string(sourceRaw), nextState)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func (w *Worker) syncPrepareReceipt(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var body struct {
		SyncID   string            `json:"sync_id"`
		OpID     string            `json:"op_id"`
		Revision int64             `json:"revision"`
		Expected tasksync.Snapshot `json:"expected"`
		Fields   []string          `json:"fields,omitempty"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	if !syncUUID.MatchString(body.SyncID) || len(body.OpID) < 8 {
		syncFail(out, 400, "回执标识无效。")
		return
	}
	var snapshot []byte
	var revision int64
	err = w.Pool.QueryRow(r.Context(), "SELECT snapshot,revision FROM sync_objects WHERE id=$1 AND connection_id=$2", body.SyncID, a.Connection).Scan(&snapshot, &revision)
	if err != nil {
		syncFail(out, 404, "任务未找到。")
		return
	}
	var expected tasksync.Snapshot
	_ = json.Unmarshal(snapshot, &expected)
	if revision != body.Revision || len(tasksync.Diff(expected, body.Expected)) != 0 {
		syncFail(out, 409, "任务已更新，请重新同步。")
		return
	}
	for _, field := range body.Fields {
		if !tasksync.Fields[field] {
			syncFail(out, 400, "回执字段无效。")
			return
		}
	}
	id := tasksync.ID(a.Connection, "receipt:"+body.OpID)
	raw, _ := json.Marshal(body.Expected)
	fieldsRaw, _ := json.Marshal(body.Fields)
	var savedObject string
	var savedRevision int64
	var savedExpected, savedFields []byte
	err = w.Pool.QueryRow(r.Context(), "INSERT INTO sync_receipts(id,connection_id,object_id,op_id,revision,expected,fields) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb) ON CONFLICT(connection_id,op_id) DO UPDATE SET op_id=EXCLUDED.op_id RETURNING object_id::text,revision,expected,fields", id, a.Connection, body.SyncID, body.OpID, revision, string(raw), string(fieldsRaw)).Scan(&savedObject, &savedRevision, &savedExpected, &savedFields)
	if err != nil {
		syncFail(out, 503, "回执暂时无法登记。")
		return
	}
	var priorExpected tasksync.Snapshot
	var priorFields []string
	_ = json.Unmarshal(savedExpected, &priorExpected)
	_ = json.Unmarshal(savedFields, &priorFields)
	if savedObject != body.SyncID || savedRevision != revision || len(tasksync.Diff(priorExpected, body.Expected)) != 0 || tasksync.Hash(priorFields) != tasksync.Hash(body.Fields) {
		syncFail(out, 409, "同一回执标识不能用于不同操作。")
		return
	}
	syncJSON(out, 201, map[string]any{"id": id})
}
func (w *Worker) syncAckReceipt(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var body struct {
		Snapshot tasksync.Snapshot `json:"snapshot"`
		Path     string            `json:"path"`
		Created  string            `json:"note_created"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	tx, err := w.Pool.Begin(r.Context())
	if err != nil {
		syncFail(out, 503, "回执暂不可用。")
		return
	}
	defer tx.Rollback(r.Context())
	var expectedRaw, fieldsRaw []byte
	var object string
	err = tx.QueryRow(r.Context(), "SELECT expected,object_id::text,fields FROM sync_receipts WHERE id=$1 AND connection_id=$2", r.PathValue("id"), a.Connection).Scan(&expectedRaw, &object, &fieldsRaw)
	var expected tasksync.Snapshot
	_ = json.Unmarshal(expectedRaw, &expected)
	var fields []string
	_ = json.Unmarshal(fieldsRaw, &fields)
	mismatch := tasksync.Diff(expected, body.Snapshot)
	if len(fields) > 0 {
		filtered := tasksync.Snapshot{}
		for _, field := range fields {
			if value, exists := mismatch[field]; exists {
				filtered[field] = value
			}
		}
		mismatch = filtered
	}
	if err != nil || len(mismatch) != 0 {
		syncFail(out, 409, "实际写入与预期不符，请核对。")
		return
	}
	if body.Path == "" || strings.Contains(body.Path, "..") {
		syncFail(out, 400, "笔记路径无效。")
		return
	}
	actualRaw, _ := json.Marshal(body.Snapshot)
	expected["path"] = body.Path
	expected["dateCreated"] = body.Created
	sourceRaw, _ := json.Marshal(expected)
	_, err = tx.Exec(r.Context(), "UPDATE sources s SET snapshot=$2::jsonb FROM sync_objects o WHERE o.id=$1 AND o.source_id=s.id", object, string(sourceRaw))
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sync_objects SET path=$2,note_created=$3 WHERE id=$1", object, body.Path, body.Created)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sync_receipts SET state='confirmed',actual=$2::jsonb WHERE id=$1", r.PathValue("id"), string(actualRaw))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		syncFail(out, 503, "回执暂时无法确认。")
		return
	}
	syncJSON(out, 200, map[string]any{"confirmed": true})
}
func (w *Worker) syncConflicts(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	rows, err := w.Pool.Query(r.Context(), "SELECT id::text,object_id::text,base_revision,base,local,remote,fields FROM sync_conflicts WHERE connection_id=$1 AND state='open' ORDER BY created_at LIMIT 100", a.Connection)
	if err != nil {
		syncFail(out, 503, "冲突暂不可读。")
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var id, obj string
		var rev int64
		var base, local, remote, fields []byte
		if rows.Scan(&id, &obj, &rev, &base, &local, &remote, &fields) != nil {
			syncFail(out, 503, "冲突读取失败。")
			return
		}
		items = append(items, map[string]any{"id": id, "sync_id": obj, "base_revision": rev, "base": json.RawMessage(base), "local": json.RawMessage(local), "remote": json.RawMessage(remote), "fields": json.RawMessage(fields)})
	}
	if rows.Err() != nil {
		syncFail(out, 503, "冲突读取失败。")
		return
	}
	syncJSON(out, 200, map[string]any{"items": items})
}
func (w *Worker) resolveSyncConflict(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var body struct {
		Keep     string `json:"keep"`
		Revision int64  `json:"revision"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	if body.Keep != "local" && body.Keep != "server" {
		syncFail(out, 400, "请选择保留版本。")
		return
	}
	tx, err := w.Pool.Begin(r.Context())
	if err != nil {
		syncFail(out, 503, "冲突暂不可用。")
		return
	}
	defer tx.Rollback(r.Context())
	var opID int64
	var currentRevision int64
	var snapshot, raw []byte
	var state string
	err = tx.QueryRow(r.Context(), "SELECT c.operation_id,c.state,o.revision,o.snapshot,p.body FROM sync_conflicts c JOIN sync_objects o ON o.id=c.object_id JOIN sync_operations p ON p.id=c.operation_id WHERE c.id=$1 AND c.connection_id=$2 FOR UPDATE OF c,o", r.PathValue("id"), a.Connection).Scan(&opID, &state, &currentRevision, &snapshot, &raw)
	if err != nil {
		syncFail(out, 404, "冲突未找到。")
		return
	}
	if state != "open" {
		syncJSON(out, 200, map[string]any{"resolved": true})
		return
	}
	if currentRevision != body.Revision {
		syncFail(out, 409, "任务已再次更新，请重新核对。")
		return
	}
	if body.Keep == "server" {
		_, err = tx.Exec(r.Context(), "UPDATE sync_operations SET state='superseded',error='' WHERE id=$1", opID)
	} else {
		var op tasksync.Operation
		_ = json.Unmarshal(raw, &op)
		_ = json.Unmarshal(snapshot, &op.Base)
		op.BaseRevision = currentRevision
		next, _ := json.Marshal(op)
		_, err = tx.Exec(r.Context(), "UPDATE sync_operations SET body=$2::jsonb,state='pending',error='',next_attempt=NOW() WHERE id=$1", opID, string(next))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sync_conflicts SET state=$2 WHERE id=$1", r.PathValue("id"), body.Keep)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		syncFail(out, 503, "冲突暂时无法保存。")
		return
	}
	syncJSON(out, 200, map[string]any{"resolved": true})
}
