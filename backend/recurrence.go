package main

import (
	"encoding/json"
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
)

func (p *integrationPlugin) getRecurrence(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT o.id::text,o.revision,o.snapshot::text,c.id::text,c.recurrence_enabled,COALESCE(s.rule_revision,0),COALESCE(s.state,''),COALESCE(s.last_error,'') FROM sync_objects o JOIN connections c ON c.id=o.connection_id LEFT JOIN recurring_series s ON s.object_id=o.id WHERE c.project_id=$1 AND o.paca_task_id=$2 AND o.canonical_id IS NULL AND NOT o.deleted AND c.enabled", req.PathParam("projectId"), req.PathParam("taskId"))
	if err != nil {
		res.Error(503, "循环设置暂时无法读取。")
		return
	}
	items := []any{}
	for _, row := range rows.Rows {
		var snapshot any
		_ = json.Unmarshal([]byte(fmt.Sprint(row[2])), &snapshot)
		periods, readErr := p.db.Query("SELECT p.occurrence_date::text,o.paca_task_id::text,p.state,p.last_error FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.series_id=$1 AND p.occurrence_date>=CURRENT_DATE ORDER BY p.occurrence_date LIMIT 200", row[0])
		if readErr != nil {
			res.Error(503, "周期清单暂时无法读取。")
			return
		}
		next := []any{}
		for _, period := range periods.Rows {
			next = append(next, map[string]any{"date": period[0], "task_id": period[1], "state": period[2], "error": period[3]})
		}
		items = append(items, map[string]any{"sync_id": row[0], "revision": row[1], "snapshot": snapshot, "connection_id": row[3], "enabled": row[4], "rule_revision": row[5], "state": row[6], "error": row[7], "periods": next})
	}
	res.JSON(200, map[string]any{"items": items, "model": "0.3.0-rc.9"})
}
func (p *integrationPlugin) setRecurrence(req *plugin.Request, res *plugin.Response) {
	body, err := plugin.JSONBody[struct {
		Connection string  `json:"connection_id"`
		Revision   int64   `json:"revision"`
		OpID       string  `json:"op_id"`
		Rule       string  `json:"recurrence"`
		Anchor     string  `json:"recurrence_anchor"`
		Scheduled  string  `json:"scheduled"`
		Due        *string `json:"due"`
	}](req)
	if err != nil || !uuidPattern.MatchString(body.Connection) || body.Revision < 1 || len(body.OpID) < 8 || len(body.OpID) > 128 || len(body.Rule) > 2048 || (body.Anchor != "scheduled" && body.Anchor != "completion") {
		res.Error(400, "请检查循环规则、开始时间和设置版本。")
		return
	}
	rows, err := p.db.Query("SELECT o.id::text,o.revision,o.snapshot::text FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.id=$1 AND c.project_id=$2 AND o.paca_task_id=$3 AND c.enabled AND c.sync_mode='enabled' AND c.recurrence_enabled AND NOT o.deleted AND o.canonical_id IS NULL", body.Connection, req.PathParam("projectId"), req.PathParam("taskId"))
	if err != nil || len(rows.Rows) != 1 {
		res.Error(409, "请先启用此项目的双向同步和循环排期，并等待任务对账。")
		return
	}
	row := rows.Rows[0]
	var base tasksync.Snapshot
	if json.Unmarshal([]byte(fmt.Sprint(row[2])), &base) != nil {
		res.Error(503, "任务设置暂时不可用。")
		return
	}
	changes := tasksync.Snapshot{"recurrence": body.Rule, "recurrence_anchor": body.Anchor, "scheduled": body.Scheduled}
	if body.Rule == "" {
		changes["recurrence"] = nil
		changes["recurrence_anchor"] = nil
	}
	if body.Due != nil {
		changes["due"] = *body.Due
	}
	op := tasksync.Operation{OpID: body.OpID, SyncID: fmt.Sprint(row[0]), BaseRevision: body.Revision, Kind: "update", Base: base, Changes: changes}
	raw, _ := json.Marshal(op)
	saved, err := p.db.Query("INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,'paca-recurrence',$3,$4,$5::jsonb) ON CONFLICT(connection_id,op_id) DO UPDATE SET op_id=EXCLUDED.op_id RETURNING body_hash,state,error", body.Connection, row[0], body.OpID, tasksync.Hash(op), string(raw))
	if err != nil || len(saved.Rows) != 1 {
		res.Error(503, "循环设置暂时无法保存。")
		return
	}
	if fmt.Sprint(saved.Rows[0][0]) != tasksync.Hash(op) {
		res.Error(409, "相同操作标识不能提交不同的循环设置。")
		return
	}
	p.audit(req, "recurrence.requested", req.PathParam("taskId"))
	res.JSON(202, map[string]any{"op_id": body.OpID, "state": saved.Rows[0][1], "error": saved.Rows[0][2]})
}
