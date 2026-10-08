package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var syncUUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

type syncAuth struct{ Connection, Project, Device, Mode string }

func syncJSON(out http.ResponseWriter, status int, data any) {
	out.Header().Set("Content-Type", "application/json; charset=utf-8")
	out.Header().Set("Cache-Control", "no-store")
	out.WriteHeader(status)
	_ = json.NewEncoder(out).Encode(data)
}
func syncFail(out http.ResponseWriter, status int, message string) {
	syncJSON(out, status, map[string]any{"error": message})
}
func syncRead(out http.ResponseWriter, r *http.Request, data any) bool {
	r.Body = http.MaxBytesReader(out, r.Body, 1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(data) != nil || decoder.Decode(new(any)) != io.EOF {
		syncFail(out, 400, "请求内容无效。")
		return false
	}
	return true
}
func (w *Worker) syncAuth(ctx context.Context, r *http.Request) (syncAuth, error) {
	a := syncAuth{Device: r.Header.Get("X-Sync-Device")}
	if !syncUUID.MatchString(a.Device) {
		return a, errors.New("设备标识无效，请重新连接。")
	}
	if err := w.control(ctx); err != nil {
		return a, err
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(strings.TrimPrefix(header, "Bearer ")) != 64 {
		return a, errors.New("配对凭据无效。")
	}
	err := w.Pool.QueryRow(ctx, "SELECT c.id::text,c.project_id::text,c.sync_mode FROM sync_credentials s JOIN connections c ON c.id=s.connection_id WHERE s.token_hash=$1 AND s.enabled AND c.enabled", rawTokenHash(strings.TrimPrefix(header, "Bearer "))).Scan(&a.Connection, &a.Project, &a.Mode)
	return a, err
}
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(out http.ResponseWriter, r *http.Request) {
		if w.control(r.Context()) != nil {
			syncFail(out, 503, "同步服务暂不可用。")
			return
		}
		syncJSON(out, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /task-sync/v1/pair", w.migratePair)
	mux.HandleFunc("GET /task-sync/v1/info", w.syncInfo)
	mux.HandleFunc("GET /task-sync/v1/lookup", w.syncLookup)
	mux.HandleFunc("GET /task-sync/v1/changes", w.syncChanges)
	mux.HandleFunc("POST /task-sync/v1/operations", w.submitSyncOperation)
	mux.HandleFunc("GET /task-sync/v1/operations/{op}", w.syncOperationStatus)
	mux.HandleFunc("POST /task-sync/v1/bindings", w.syncBinding)
	mux.HandleFunc("POST /task-sync/v1/receipts", w.syncPrepareReceipt)
	mux.HandleFunc("POST /task-sync/v1/receipts/{id}/ack", w.syncAckReceipt)
	mux.HandleFunc("GET /task-sync/v1/conflicts", w.syncConflicts)
	mux.HandleFunc("POST /task-sync/v1/conflicts/{id}/resolve", w.resolveSyncConflict)
	mux.HandleFunc("/task-sync/v1/checkin/", w.proxyCheckinSync)
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("X-Content-Type-Options", "nosniff")
		out.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(out, r)
	})
}
func (w *Worker) syncLookup(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var object, path, created string
	var revision int64
	var snapshot []byte
	err = w.Pool.QueryRow(r.Context(), "SELECT o.id::text,o.path,o.note_created,o.revision,o.snapshot FROM sync_objects o WHERE o.connection_id=$1 AND o.path=$2 AND o.note_created=$3 AND NOT o.deleted", a.Connection, r.URL.Query().Get("path"), r.URL.Query().Get("note_created")).Scan(&object, &path, &created, &revision, &snapshot)
	if err != nil {
		syncFail(out, 404, "任务尚未关联。")
		return
	}
	syncJSON(out, 200, map[string]any{"sync_id": object, "path": path, "note_created": created, "revision": revision, "snapshot": json.RawMessage(snapshot)})
}
func (w *Worker) syncInfo(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效或插件已停用。")
		return
	}
	c, err := w.syncConfig(r.Context(), a.Connection)
	if err != nil {
		syncFail(out, 503, "同步设置暂不可用。")
		return
	}
	syncJSON(out, 200, map[string]any{"connection_id": a.Connection, "project_id": a.Project, "mode": a.Mode, "timezone": c.Timezone, "status_map": c.Statuses, "priority_map": c.Priorities})
}
func (w *Worker) syncChanges(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效或插件已停用。")
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		syncFail(out, 400, "同步进度无效。")
		return
	}
	if a.Mode != "enabled" {
		syncJSON(out, 200, map[string]any{"items": []any{}, "next_cursor": after, "has_more": false, "mode": a.Mode})
		return
	}
	rows, err := w.Pool.Query(r.Context(), "SELECT cursor,payload FROM sync_changes WHERE connection_id=$1 AND cursor>$2 ORDER BY cursor LIMIT 100", a.Connection, after)
	if err != nil {
		syncFail(out, 503, "变更暂时无法读取。")
		return
	}
	defer rows.Close()
	items := []tasksync.Change{}
	next := after
	for rows.Next() {
		var cursor int64
		var raw []byte
		if rows.Scan(&cursor, &raw) != nil {
			syncFail(out, 503, "变更读取失败。")
			return
		}
		var item tasksync.Change
		if json.Unmarshal(raw, &item) != nil {
			syncFail(out, 503, "变更读取失败。")
			return
		}
		item.Cursor = cursor
		items = append(items, item)
		next = cursor
	}
	if rows.Err() != nil {
		syncFail(out, 503, "变更读取失败。")
		return
	}
	syncJSON(out, 200, map[string]any{"items": items, "next_cursor": next, "has_more": len(items) == 100, "mode": a.Mode})
}
func (w *Worker) migratePair(out http.ResponseWriter, r *http.Request) {
	if w.CheckinURL == "" || w.CheckinSecret == "" || w.control(r.Context()) != nil {
		syncFail(out, 503, "配对迁移暂不可用。")
		return
	}
	var body struct {
		Token  string `json:"token"`
		Device string `json:"device_id"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	if len(body.Token) != 64 || !syncUUID.MatchString(body.Device) {
		syncFail(out, 400, "配对凭据或设备标识无效。")
		return
	}
	raw, _ := json.Marshal(map[string]any{"token": body.Token})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", w.CheckinURL+"/internal/v1/pairing-info", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+w.CheckinSecret)
	req.Header.Set("Content-Type", "application/json")
	reply, err := w.HTTP.Do(req)
	if err != nil {
		syncFail(out, 503, "旧配对暂时无法验证。")
		return
	}
	defer reply.Body.Close()
	var scope struct {
		Project    string `json:"project_id"`
		Connection string `json:"connection_id"`
	}
	if reply.StatusCode != 200 || json.NewDecoder(io.LimitReader(reply.Body, 4096)).Decode(&scope) != nil {
		syncFail(out, 401, "原配对已失效，请重新配对。")
		return
	}
	var valid bool
	err = w.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM connections WHERE id=$1 AND project_id=$2 AND enabled)", scope.Connection, scope.Project).Scan(&valid)
	if err != nil || !valid {
		syncFail(out, 403, "配对没有任务来源权限。")
		return
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		syncFail(out, 503, "配对暂不可用。")
		return
	}
	plain := hex.EncodeToString(token)
	id := tasksync.ID(scope.Connection, "credential:"+plain)
	_, err = w.Pool.Exec(r.Context(), "INSERT INTO sync_credentials(id,connection_id,token_hash) VALUES($1,$2,$3)", id, scope.Connection, rawTokenHash(plain))
	if err != nil {
		syncFail(out, 503, "配对暂时无法保存。")
		return
	}
	syncJSON(out, 201, map[string]any{"token": plain, "connection_id": scope.Connection, "project_id": scope.Project})
}
func (w *Worker) proxyCheckinSync(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效或插件已停用。")
		return
	}
	if w.CheckinURL == "" || w.CheckinSecret == "" {
		syncFail(out, 503, "打卡服务未启用。")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/task-sync/v1/checkin/")
	if !regexp.MustCompile(`^(info|changes|sources/[a-fA-F0-9-]{36}|media/[a-fA-F0-9-]{36}|receipts|receipts/[a-fA-F0-9-]{36}/ack|conflicts|conflicts/[a-fA-F0-9-]{36}/resolve)$`).MatchString(suffix) {
		syncFail(out, 404, "接口未找到。")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, w.CheckinURL+"/internal/v1/sync/"+suffix+"?"+r.URL.RawQuery, io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		syncFail(out, 400, "请求无效。")
		return
	}
	req.Header.Set("Authorization", "Bearer "+w.CheckinSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sync-Project", a.Project)
	req.Header.Set("X-Sync-Connection", a.Connection)
	req.Header.Set("X-Sync-Device", a.Device)
	reply, err := w.HTTP.Do(req)
	if err != nil {
		syncFail(out, 503, "打卡同步暂不可用。")
		return
	}
	defer reply.Body.Close()
	out.Header().Set("Content-Type", reply.Header.Get("Content-Type"))
	out.Header().Set("Cache-Control", "no-store")
	out.WriteHeader(reply.StatusCode)
	_, _ = io.Copy(out, io.LimitReader(reply.Body, 11*1024*1024))
}
func (w *Worker) syncBinding(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	if a.Mode != "enabled" {
		syncFail(out, 409, "任务同步尚未启用。")
		return
	}
	var body struct {
		SyncID   string `json:"sync_id"`
		Path     string `json:"path"`
		Created  string `json:"note_created"`
		Action   string `json:"action"`
		Revision int64  `json:"revision"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	if !syncUUID.MatchString(body.SyncID) {
		syncFail(out, 400, "任务标识无效。")
		return
	}
	tx, err := w.Pool.Begin(r.Context())
	if err != nil {
		syncFail(out, 503, "关联暂不可用。")
		return
	}
	defer tx.Rollback(r.Context())
	var state, path, birth string
	var owner *string
	var revision int64
	var snapshotRaw []byte
	var task, ref string
	err = tx.QueryRow(r.Context(), "SELECT binding_state,path,note_created,binding_owner,revision,snapshot,paca_task_id::text,source_ref FROM sync_objects WHERE id=$1 AND connection_id=$2 AND NOT deleted FOR UPDATE", body.SyncID, a.Connection).Scan(&state, &path, &birth, &owner, &revision, &snapshotRaw, &task, &ref)
	if err != nil {
		syncFail(out, 404, "任务未找到。")
		return
	}
	if state == "bound" {
		syncJSON(out, 200, map[string]any{"state": state, "path": path, "note_created": birth})
		return
	}
	if body.Action == "claim" {
		if state == "creating" && (owner == nil || *owner != a.Device) {
			syncJSON(out, 200, map[string]any{"state": "pending", "message": "另一设备的创建结果尚未确认。"})
			return
		}
		_, err = tx.Exec(r.Context(), "UPDATE sync_objects SET binding_state='creating',binding_owner=$2,binding_until=NOW()+INTERVAL '2 minutes' WHERE id=$1", body.SyncID, a.Device)
		if err != nil || tx.Commit(r.Context()) != nil {
			syncFail(out, 503, "创建意图暂时无法保存。")
			return
		}
		syncJSON(out, 200, map[string]any{"state": "claimed"})
		return
	}
	if body.Action != "confirm" || owner == nil || *owner != a.Device || body.Path == "" || body.Created == "" || strings.Contains(body.Path, "..") || strings.HasPrefix(body.Path, "/") {
		syncFail(out, 409, "创建关联需要核对。")
		return
	}
	var snapshot tasksync.Snapshot
	_ = json.Unmarshal(snapshotRaw, &snapshot)
	snapshot["path"] = body.Path
	snapshot["dateCreated"] = body.Created
	raw, _ := json.Marshal(snapshot)
	var sourceID int64
	err = tx.QueryRow(r.Context(), "SELECT id FROM sources WHERE connection_id=$1 AND paca_task_id=$2 AND state<>'superseded' ORDER BY id DESC LIMIT 1", a.Connection, task).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(r.Context(), "INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot) VALUES($1,'sync',$2,$3,$4,'linked',$5::jsonb) RETURNING id", a.Connection, body.Path, task, ref, string(raw)).Scan(&sourceID)
	} else if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sources SET snapshot=$2::jsonb,state='linked' WHERE id=$1", sourceID, string(raw))
	}
	if err != nil {
		syncFail(out, 409, "任务关联存在冲突，请核对。")
		return
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,'sync',$2,$3) ON CONFLICT(connection_id,vault_key,path) DO NOTHING", a.Connection, body.Path, sourceID)
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sync_objects SET path=$2,note_created=$3,binding_state='bound',source_id=$4,updated_at=NOW() WHERE id=$1", body.SyncID, body.Path, body.Created, sourceID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		syncFail(out, 503, "关联暂时无法保存。")
		return
	}
	syncJSON(out, 200, map[string]any{"state": "bound", "path": body.Path, "note_created": body.Created, "revision": revision})
}
