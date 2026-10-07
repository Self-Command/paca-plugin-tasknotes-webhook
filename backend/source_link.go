package main

import (
	"encoding/json"
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
)

// Read-only, project-permission-scoped integration boundary; no secret or cross-schema SQL.
func (p *integrationPlugin) sourceLink(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT s.id,s.connection_id::text,s.external_ref,COALESCE(s.snapshot->>'path',s.source_key),s.snapshot::text,s.event_at::text,s.state,(SELECT COALESCE(json_agg(a.path),'[]'::json)::text FROM path_aliases a WHERE a.source_id=s.id) FROM sources s JOIN connections c ON c.id=s.connection_id WHERE c.project_id=$1 AND s.paca_task_id=$2 AND s.state='linked'", req.PathParam("projectId"), req.PathParam("taskId"))
	if err != nil {
		res.Error(503, "source lookup unavailable")
		return
	}
	if len(rows.Rows) == 0 {
		res.Error(404, "no linked TaskNotes source")
		return
	}
	if len(rows.Rows) != 1 {
		res.Error(409, "multiple source links require manual resolution")
		return
	}
	r := rows.Rows[0]
	var snapshot any
	_ = json.Unmarshal([]byte(fmt.Sprint(r[4])), &snapshot)
	var aliases any
	_ = json.Unmarshal([]byte(fmt.Sprint(r[7])), &aliases)
	res.JSON(200, map[string]any{"aliases": aliases, "source_id": r[0], "connection_id": r[1], "source_ref": r[2], "path": r[3], "snapshot": snapshot, "event_at": r[5], "state": r[6]})
}
