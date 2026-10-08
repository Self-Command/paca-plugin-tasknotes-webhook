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
		native,lookupErr:=p.db.Query("SELECT o.connection_id::text,o.source_ref,o.path,o.note_created,o.snapshot::text,o.path_aliases::text FROM sync_objects o JOIN connections c ON c.id=o.connection_id WHERE c.project_id=$1 AND o.paca_task_id=$2 AND NOT o.deleted AND c.sync_mode='enabled'",req.PathParam("projectId"),req.PathParam("taskId"))
		if lookupErr!=nil{res.Error(503,"任务来源暂时无法读取。");return};if len(native.Rows)!=1{res.Error(404,"任务尚未关联笔记库。");return};r:=native.Rows[0];var snapshot map[string]any;_=json.Unmarshal([]byte(fmt.Sprint(r[4])),&snapshot);snapshot["path"]=r[2];snapshot["dateCreated"]=r[3];var aliases any;_=json.Unmarshal([]byte(fmt.Sprint(r[5])),&aliases);res.JSON(200,map[string]any{"connection_id":r[0],"source_ref":r[1],"path":r[2],"snapshot":snapshot,"aliases":aliases,"state":"linked"});return
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
