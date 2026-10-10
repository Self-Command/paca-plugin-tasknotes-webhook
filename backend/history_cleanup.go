package main

import (
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"time"
)

// Retain tombstone identities and delivery hashes: late/replayed messages must not
// recreate a deleted task. A cutoff prevents clearing events received after review.
const historyCandidates = `WITH scope AS (
 SELECT id,COALESCE(NULLIF($3,'')::timestamptz,NOW()) AS cutoff FROM connections WHERE project_id=$1 AND id=$2
), old_objects AS (
 SELECT o.* FROM sync_objects o JOIN scope c ON c.id=o.connection_id
 WHERE o.deleted AND o.history_cleared_at IS NULL AND o.updated_at<=c.cutoff
 AND NOT EXISTS(SELECT 1 FROM sync_operations x WHERE x.object_id=o.id AND x.state IN('pending','retry','sending','waiting_period','uncertain'))
), old_sources AS (
 SELECT s.* FROM sources s JOIN scope c ON c.id=s.connection_id
 WHERE s.history_cleared_at IS NULL AND s.updated_at<=c.cutoff
 AND (s.state IN('deleted','superseded') OR EXISTS(SELECT 1 FROM old_objects o WHERE o.source_id=s.id OR o.paca_task_id=s.paca_task_id))
 AND NOT EXISTS(SELECT 1 FROM sync_objects o WHERE o.connection_id=s.connection_id AND (o.source_id=s.id OR o.paca_task_id=s.paca_task_id) AND NOT o.deleted)
 AND NOT EXISTS(SELECT 1 FROM sync_operations x JOIN sync_objects o ON o.id=x.object_id WHERE o.connection_id=s.connection_id AND (o.source_id=s.id OR o.paca_task_id=s.paca_task_id) AND x.state IN('pending','retry','sending','waiting_period','uncertain'))
 AND NOT EXISTS(SELECT 1 FROM inbox i WHERE i.connection_id=s.connection_id AND i.state='pending' AND i.body#>>'{data,task,path}'=COALESCE(s.snapshot->>'path',s.source_key))
), old_inbox AS (
 SELECT i.* FROM inbox i JOIN scope c ON c.id=i.connection_id
 WHERE i.history_cleared_at IS NULL AND i.updated_at<=c.cutoff AND (i.lease_until IS NULL OR i.lease_until<NOW())
 AND (i.state IN('applied','stale','superseded') OR (i.state IN('error','conflict','uncertain') AND EXISTS(
 SELECT 1 FROM old_sources s WHERE i.body#>>'{data,task,path}'=COALESCE(s.snapshot->>'path',s.source_key)
 AND COALESCE(i.body#>>'{data,task,dateCreated}','')=COALESCE(s.snapshot->>'dateCreated',''))))
 AND NOT EXISTS(SELECT 1 FROM sync_operations x WHERE x.inbox_id=i.id AND x.state IN('pending','retry','sending','waiting_period','uncertain'))
)`

func (p *integrationPlugin) historyCleanupPreview(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query(historyCandidates+` SELECT (SELECT COUNT(*) FROM old_objects),(SELECT COUNT(*) FROM old_inbox),(SELECT COUNT(*) FROM old_sources),to_char(cutoff AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM scope`, req.PathParam("projectId"), req.PathParam("id"), "")
	if err != nil {
		res.Error(503, "清理范围暂时无法读取。")
		return
	}
	if len(rows.Rows) != 1 {
		res.Error(404, "来源连接未找到。")
		return
	}
	r := rows.Rows[0]
	res.JSON(200, map[string]any{"deleted_tasks": r[0], "processed_events": r[1], "obsolete_sources": r[2], "cutoff": r[3]})
}

func (p *integrationPlugin) historyCleanup(req *plugin.Request, res *plugin.Response) {
	body, err := plugin.JSONBody[struct {
		Cutoff  string `json:"cutoff"`
		Confirm bool   `json:"confirm"`
	}](req)
	cutoff, parseErr := time.Parse(time.RFC3339Nano, body.Cutoff)
	if err != nil || parseErr != nil || !body.Confirm || cutoff.After(time.Now().Add(time.Minute)) {
		res.Error(400, "请先核对清理范围并确认。")
		return
	}
	// One statement makes all history changes atomic within this connection.
	rows, err := p.db.Query(historyCandidates+`, cleared_objects AS (
 UPDATE sync_objects o SET snapshot='{}',paca_snapshot='{}',path_aliases='[]',last_error='',history_cleared_at=NOW(),revision=o.revision+1
 FROM old_objects x WHERE o.id=x.id AND o.deleted AND o.updated_at<= (SELECT cutoff FROM scope)
 RETURNING o.id,o.connection_id,o.paca_task_id,o.revision,o.kind,o.path,o.note_created,o.source_ref
), cleared_sources AS (
 UPDATE sources s SET snapshot=jsonb_build_object('path',COALESCE(s.snapshot->>'path',s.source_key),'dateCreated',COALESCE(s.snapshot->>'dateCreated','')),source_tags='[]',state='deleted',history_cleared_at=NOW()
 FROM old_sources x WHERE s.id=x.id RETURNING s.id
), cleared_inbox AS (
 UPDATE inbox i SET body='{}',error='',state='cleared',history_cleared_at=NOW() FROM old_inbox x WHERE i.id=x.id RETURNING i.id
), redacted_changes AS (
 UPDATE sync_changes ch SET payload=jsonb_build_object('sync_id',o.id,'task_id',o.paca_task_id,'revision',ch.revision,'kind',o.kind,'deleted',TRUE,'path',o.path,'note_created',o.note_created,'source_ref',o.source_ref,'snapshot','{}'::jsonb,'warnings','[]'::jsonb)
 FROM cleared_objects o WHERE ch.object_id=o.id RETURNING ch.cursor
), fresh_changes AS (
 INSERT INTO sync_changes(connection_id,object_id,revision,payload)
 SELECT o.connection_id,o.id,o.revision,jsonb_build_object('sync_id',o.id,'task_id',o.paca_task_id,'revision',o.revision,'kind',o.kind,'deleted',TRUE,'path',o.path,'note_created',o.note_created,'source_ref',o.source_ref,'snapshot','{}'::jsonb,'warnings','[]'::jsonb) FROM cleared_objects o RETURNING cursor
), cleared_operations AS (
 UPDATE sync_operations x SET body='{}',error='',state=CASE WHEN x.state='conflict' THEN 'superseded' ELSE x.state END
 WHERE x.object_id IN(SELECT id FROM cleared_objects) AND x.state IN('applied','superseded','failed','conflict') RETURNING x.id
), cleared_conflicts AS (
 UPDATE sync_conflicts x SET base='{}',local='{}',remote='{}',fields='[]',state='cleared' WHERE x.object_id IN(SELECT id FROM cleared_objects) RETURNING x.id
), cleared_receipts AS (
 UPDATE sync_receipts x SET expected='{}',actual=NULL,fields='[]' WHERE x.object_id IN(SELECT id FROM cleared_objects) AND x.state='confirmed' RETURNING x.id
)
SELECT (SELECT COUNT(*) FROM cleared_objects),(SELECT COUNT(*) FROM cleared_inbox),(SELECT COUNT(*) FROM cleared_sources) FROM scope`, req.PathParam("projectId"), req.PathParam("id"), body.Cutoff)
	if err != nil {
		res.Error(503, "旧历史暂时无法清理，请重试。")
		return
	}
	if len(rows.Rows) != 1 {
		res.Error(404, "来源连接未找到。")
		return
	}
	r := rows.Rows[0]
	p.audit(req, "history.cleared", req.PathParam("id")+":"+fmt.Sprint(r))
	res.JSON(200, map[string]any{"deleted_tasks": r[0], "processed_events": r[1], "obsolete_sources": r[2]})
}
