package main

import (
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// Only a persisted identity-specific tombstone proves deletion.
func (p *integrationPlugin) sourceStatus(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT o.connection_id::text,o.source_ref,o.deleted,o.path FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.project_id=$1 AND o.paca_task_id=$2 AND o.canonical_id IS NULL", req.PathParam("projectId"), req.PathParam("taskId"))
	if err != nil {
		res.Error(503, "来源暂时无法读取。")
		return
	}
	if len(rows.Rows) == 0 {
		rows, err = p.db.Query("SELECT s.connection_id::text,s.external_ref,(s.state='deleted'),COALESCE(s.snapshot->>'path',s.source_key) FROM sources s JOIN connections c ON c.id=s.connection_id WHERE c.project_id=$1 AND s.paca_task_id=$2", req.PathParam("projectId"), req.PathParam("taskId"))
	}
	if err != nil {
		res.Error(503, "来源暂时无法读取。")
		return
	}
	if len(rows.Rows) != 1 {
		res.JSON(200, map[string]any{"state": "unlinked", "task_id": req.PathParam("taskId")})
		return
	}
	r := rows.Rows[0]
	state := "active"
	if fmt.Sprint(r[2]) == "true" {
		state = "deleted"
	}
	res.JSON(200, map[string]any{"state": state, "connection_id": r[0], "source_ref": r[1], "path": r[3], "task_id": req.PathParam("taskId")})
}

func (p *integrationPlugin) noteBindings(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT o.path,o.note_created,COALESCE(o.snapshot->>'title','任务笔记') FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.project_id=$1 AND c.id=$2 AND NOT o.deleted AND o.canonical_id IS NULL AND o.path<>'' AND o.note_created<>'' ORDER BY o.path", req.PathParam("projectId"), req.PathParam("id"))
	if err != nil {
		res.Error(503, "任务笔记暂不可用。")
		return
	}
	items := []any{}
	for _, r := range rows.Rows {
		items = append(items, map[string]any{"path": r[0], "note_created": r[1], "title": r[2]})
	}
	res.JSON(200, map[string]any{"items": items})
}
