package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"strings"
)

func (p *integrationPlugin) syncConfig(req *plugin.Request, res *plugin.Response) {
	body, err := plugin.JSONBody[struct {
		Mode     string            `json:"mode"`
		Vault    string            `json:"vault_id"`
		Revision int               `json:"revision"`
		Reverse  map[string]string `json:"reverse_status_map"`
		TaskTag  *string           `json:"task_tag"`
	}](req)
	if err != nil || (body.Mode != "off" && body.Mode != "preview" && body.Mode != "enabled") || len(body.Vault) > 128 || body.Revision < 1 {
		res.Error(400, "请检查同步模式、笔记库和设置版本。")
		return
	}
	for id, value := range body.Reverse {
		if (!uuidPattern.MatchString(id) && id != "@none") || value == "" || len(value) > 100 {
			res.Error(400, "请检查状态映射。")
			return
		}
	}
	if body.TaskTag != nil && (*body.TaskTag == "" || len(*body.TaskTag) > 128 || strings.TrimSpace(*body.TaskTag) != *body.TaskTag) {
		res.Error(400, "请填写 TaskNotes 使用的任务标签。")
		return
	}
	raw, _ := json.Marshal(body.Reverse)
	if body.Reverse == nil {
		raw = []byte("{}")
	}
	if body.Mode == "enabled" {
		ready, checkErr := p.db.Query("SELECT EXISTS(SELECT 1 FROM sync_scan_state s JOIN connections c ON c.id=s.connection_id WHERE c.id=$1 AND c.project_id=$2 AND s.last_complete IS NOT NULL AND s.last_error='') AND NOT EXISTS(SELECT 1 FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.id=$1 AND c.project_id=$2 AND NOT o.deleted AND o.last_error<>'')", req.PathParam("id"), req.PathParam("projectId"))
		if checkErr != nil || len(ready.Rows) != 1 || fmt.Sprint(ready.Rows[0][0]) != "true" {
			res.Error(409, "请先完成任务清单和状态对应检查。")
			return
		}
	}
	changed, err := p.db.Exec("UPDATE connections SET sync_mode=$1,vault_id=COALESCE(NULLIF($2,''),vault_id),reverse_status_map=$3::jsonb,task_tag=COALESCE($7,task_tag),revision=revision+1 WHERE project_id=$4 AND id=$5 AND revision=$6", body.Mode, body.Vault, string(raw), req.PathParam("projectId"), req.PathParam("id"), body.Revision, body.TaskTag)
	if err != nil {
		res.Error(503, "同步设置暂时无法保存。")
		return
	}
	if changed != 1 {
		res.Error(409, "设置已发生变化，请刷新。")
		return
	}
	p.audit(req, "sync.configuration.updated", req.PathParam("id"))
	_, _ = p.db.Exec("INSERT INTO sync_dirty(project_id) VALUES($1) ON CONFLICT(project_id) DO UPDATE SET updated_at=NOW()", req.PathParam("projectId"))
	res.JSON(200, map[string]any{"revision": body.Revision + 1, "mode": body.Mode})
}
func (p *integrationPlugin) syncPreview(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT o.id::text,o.paca_task_id::text,o.revision,o.snapshot::text,o.path,o.deleted,o.last_error,c.sync_mode FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.project_id=$1 AND c.id=$2 ORDER BY o.updated_at,o.id LIMIT 200", req.PathParam("projectId"), req.PathParam("id"))
	if err != nil {
		res.Error(503, "任务清单暂时无法读取。")
		return
	}
	items := []any{}
	for _, r := range rows.Rows {
		var snapshot any
		_ = json.Unmarshal([]byte(fmt.Sprint(r[3])), &snapshot)
		items = append(items, map[string]any{"sync_id": r[0], "task_id": r[1], "revision": r[2], "snapshot": snapshot, "path": r[4], "deleted": r[5], "warning": r[6], "mode": r[7]})
	}
	counts, err := p.db.Query("SELECT COUNT(*) FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.id=$1 AND c.project_id=$2", req.PathParam("id"), req.PathParam("projectId"))
	if err != nil {
		res.Error(503, "任务清单暂时无法读取。")
		return
	}
	res.JSON(200, map[string]any{"items": items, "total": counts.Rows[0][0], "preview_limit": 200})
}
func (p *integrationPlugin) syncPair(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT id FROM connections WHERE id=$1 AND project_id=$2 AND enabled", req.PathParam("id"), req.PathParam("projectId"))
	if err != nil || len(rows.Rows) != 1 {
		res.Error(404, "来源连接未找到。")
		return
	}
	id, err := newUUID()
	if err != nil {
		res.Error(503, "配对暂时不可用。")
		return
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		res.Error(503, "配对暂时不可用。")
		return
	}
	plain := hex.EncodeToString(bytes)
	sum := sha256.Sum256([]byte(plain))
	_, err = p.db.Exec("INSERT INTO sync_credentials(id,connection_id,token_hash) VALUES($1,$2,$3)", id, req.PathParam("id"), hex.EncodeToString(sum[:]))
	if err != nil {
		res.Error(503, "配对暂时无法保存。")
		return
	}
	res.JSON(201, map[string]any{"id": id, "token": plain})
}
func (p *integrationPlugin) revokeSync(req *plugin.Request, res *plugin.Response) {
	n, err := p.db.Exec("UPDATE sync_credentials s SET enabled=FALSE FROM connections c WHERE s.connection_id=c.id AND c.project_id=$1 AND c.id=$2 AND s.id=$3", req.PathParam("projectId"), req.PathParam("id"), req.PathParam("credential"))
	if err != nil {
		res.Error(503, "配对暂时无法撤销。")
		return
	}
	res.JSON(200, map[string]any{"revoked": n == 1})
}
func (p *integrationPlugin) syncDirty(evt *plugin.Event) {
	_, _ = p.db.Exec("INSERT INTO sync_dirty(project_id) SELECT DISTINCT project_id FROM connections WHERE sync_mode<>'off' ON CONFLICT(project_id) DO UPDATE SET updated_at=NOW()")
}
