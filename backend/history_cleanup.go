package main

import (
	"encoding/json"
	"fmt"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"strings"
	"time"
)

// Preserve identities and hashes. Selected records may abandon queued changes;
// leased work and anything newer than the review are always protected.
const historyCandidates = `WITH scope AS MATERIALIZED (
 SELECT id,COALESCE(NULLIF($3,'')::timestamptz,NOW()) AS cutoff,$4::text AS mode,$5::jsonb AS selection FROM connections WHERE project_id=$1 AND id=$2
), scoped_operations AS MATERIALIZED (
 SELECT x.* FROM sync_operations x JOIN scope c ON c.id=x.connection_id
), old_objects AS (
 SELECT o.* FROM sync_objects o JOIN scope c ON c.id=o.connection_id
 WHERE o.deleted AND o.history_cleared_at IS NULL AND o.updated_at<=c.cutoff
 AND (c.mode='history' OR (c.mode='sources' AND EXISTS(SELECT 1 FROM sources s WHERE s.connection_id=c.id AND (s.id=o.source_id OR s.paca_task_id=o.paca_task_id) AND c.selection @> to_jsonb(ARRAY[s.id]))))
 AND NOT EXISTS(SELECT 1 FROM scoped_operations x WHERE x.object_id=o.id AND (x.lease_until>NOW() OR (x.state IN('pending','retry','sending','waiting_period','uncertain') AND (c.mode='history' OR x.created_at>c.cutoff))))
), old_sources AS (
 SELECT s.* FROM sources s JOIN scope c ON c.id=s.connection_id
 WHERE s.history_cleared_at IS NULL AND s.updated_at<=c.cutoff
 AND (c.mode='history' OR (c.mode='sources' AND c.selection @> to_jsonb(ARRAY[s.id])))
 AND (s.state IN('deleted','superseded') OR EXISTS(SELECT 1 FROM old_objects o WHERE o.source_id=s.id OR o.paca_task_id=s.paca_task_id))
 AND NOT EXISTS(SELECT 1 FROM sync_objects o WHERE o.connection_id=s.connection_id AND (o.source_id=s.id OR o.paca_task_id=s.paca_task_id) AND NOT o.deleted)
 AND NOT EXISTS(SELECT 1 FROM scoped_operations x JOIN sync_objects o ON o.id=x.object_id WHERE (o.source_id=s.id OR o.paca_task_id=s.paca_task_id) AND (x.lease_until>NOW() OR (x.state IN('pending','retry','sending','waiting_period','uncertain') AND (c.mode='history' OR x.created_at>c.cutoff))))
 AND NOT EXISTS(SELECT 1 FROM inbox i WHERE i.connection_id=s.connection_id AND i.history_cleared_at IS NULL AND i.state IN('pending','error','uncertain') AND i.body#>>'{data,task,path}'=COALESCE(s.snapshot->>'path',s.source_key) AND COALESCE(i.body#>>'{data,task,dateCreated}','')=COALESCE(s.snapshot->>'dateCreated','') AND (i.lease_until>NOW() OR c.mode='history' OR i.updated_at>c.cutoff))
), old_inbox AS (
 SELECT i.* FROM inbox i JOIN scope c ON c.id=i.connection_id
 WHERE i.history_cleared_at IS NULL AND i.updated_at<=c.cutoff AND (i.lease_until IS NULL OR i.lease_until<NOW())
 AND ((c.mode='events' AND c.selection @> to_jsonb(ARRAY[i.id]) AND i.state IN('applied','stale','superseded','error','conflict','uncertain'))
 OR (c.mode='history' AND i.state IN('applied','stale','superseded'))
 OR ((i.state IN('error','conflict','uncertain','applied','stale','superseded') OR (c.mode='sources' AND i.state='pending')) AND EXISTS(
 SELECT 1 FROM old_sources s WHERE i.body#>>'{data,task,path}'=COALESCE(s.snapshot->>'path',s.source_key)
 AND COALESCE(i.body#>>'{data,task,dateCreated}','')=COALESCE(s.snapshot->>'dateCreated',''))))
 AND NOT EXISTS(SELECT 1 FROM scoped_operations x WHERE x.inbox_id=i.id AND (x.lease_until>NOW() OR (x.state IN('pending','retry','sending','waiting_period','uncertain') AND (c.mode='history' OR x.created_at>c.cutoff))))
)`

type historySelection struct {
	Mode string `json:"mode"`
	IDs []int64 `json:"ids"`
	Cutoff string `json:"cutoff"`
	Confirm bool `json:"confirm"`
}

func (b *historySelection) validate() bool {
	if b.Mode == "" { b.Mode = "history" }
	if b.Mode == "history" { return len(b.IDs) == 0 }
	if (b.Mode != "events" && b.Mode != "sources") || len(b.IDs) == 0 || len(b.IDs) > 100 { return false }
	seen := map[int64]bool{}
	for _, id := range b.IDs { if id < 1 || seen[id] { return false }; seen[id] = true }
	return true
}

func (p *integrationPlugin) historyCleanupPreview(req *plugin.Request, res *plugin.Response) {
	p.previewHistory(req, res, historySelection{Mode:"history", IDs:[]int64{}})
}

func (p *integrationPlugin) historySelectionPreview(req *plugin.Request, res *plugin.Response) {
	body, err := plugin.JSONBody[historySelection](req)
	if err != nil || !body.validate() { res.Error(400,"请选择当前页面的记录。"); return }
	p.previewHistory(req, res, body)
}

func (p *integrationPlugin) previewHistory(req *plugin.Request, res *plugin.Response, body historySelection) {
	ids, _ := json.Marshal(body.IDs)
	rows, err := p.db.Query("SELECT * FROM ("+historyCandidates+` SELECT (SELECT COUNT(*) FROM old_objects),(SELECT COUNT(*) FROM old_inbox),(SELECT COUNT(*) FROM old_sources),to_char(cutoff AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
 (SELECT COUNT(*) FROM old_inbox i WHERE i.state IN('error','conflict','uncertain') AND EXISTS(SELECT 1 FROM sync_objects o WHERE o.connection_id=i.connection_id AND NOT o.deleted AND (EXISTS(SELECT 1 FROM scoped_operations x WHERE x.inbox_id=i.id AND x.object_id=o.id) OR o.path=i.body#>>'{data,task,path}')))
 FROM scope) preview`, req.PathParam("projectId"), req.PathParam("id"), "", body.Mode, string(ids))
	if err != nil { res.Error(503, "清理范围暂时无法读取。"); return }
	if len(rows.Rows) != 1 { res.Error(404, "来源连接未找到。"); return }
	r := rows.Rows[0]
	res.JSON(200, map[string]any{"deleted_tasks": r[0], "processed_events": r[1], "obsolete_sources": r[2], "cutoff": r[3], "live_changes":r[4]})
}

func (p *integrationPlugin) historyCleanup(req *plugin.Request, res *plugin.Response) {
	body, err := plugin.JSONBody[historySelection](req)
	cutoff, parseErr := time.Parse(time.RFC3339Nano, body.Cutoff)
	if err != nil || parseErr != nil || !body.validate() || !body.Confirm || cutoff.After(time.Now().Add(time.Minute)) {
		res.Error(400, "请先核对清理范围并确认。"); return
	}
	id, err := newUUID()
	if err != nil { res.Error(503, "旧历史暂时无法清理。"); return }
	ids, _ := json.Marshal(body.IDs)
	// Share the worker's connection lock and lock operation rows before deciding
	// eligibility. This excludes an operation claimed just before this request.
	locked := strings.Replace(historyCandidates,"WHERE project_id=$1 AND id=$2\n", "WHERE project_id=$1 AND id=$2 AND pg_try_advisory_xact_lock(hashtextextended(id::text,0))\n",1)
	locked = strings.Replace(locked, "), old_objects AS (", " FOR UPDATE OF x), old_objects AS (", 1)
	locked = strings.Replace(locked, "), old_sources AS (", " FOR UPDATE OF o), old_sources AS (", 1)
	locked = strings.Replace(locked, "), old_inbox AS (", " FOR UPDATE OF s), old_inbox AS (", 1)
	locked = strings.TrimSuffix(locked, "\n)") + "\n FOR UPDATE OF i)"
	n, err := p.db.Exec(locked+`, cleared_objects AS (
 UPDATE sync_objects o SET snapshot='{}',paca_snapshot='{}',path_aliases='[]',last_error='',history_cleared_at=NOW(),revision=o.revision+1
 FROM old_objects x WHERE o.id=x.id AND o.deleted AND o.updated_at<= (SELECT cutoff FROM scope)
 RETURNING o.id,o.connection_id,o.paca_task_id,o.revision,o.kind,o.path,o.note_created,o.source_ref
), cleared_sources AS (
 UPDATE sources s SET snapshot=jsonb_build_object('path',COALESCE(s.snapshot->>'path',s.source_key),'dateCreated',COALESCE(s.snapshot->>'dateCreated','')),source_tags='[]',state='deleted',history_cleared_at=NOW()
 FROM old_sources x WHERE s.id=x.id RETURNING s.id
), cleared_inbox AS (
 UPDATE inbox i SET body='{}',error='',state='cleared',lease_owner=NULL,lease_until=NULL,history_cleared_at=NOW() FROM old_inbox x WHERE i.id=x.id RETURNING i.id
), redacted_changes AS (
 UPDATE sync_changes ch SET payload=jsonb_build_object('sync_id',o.id,'task_id',o.paca_task_id,'revision',ch.revision,'kind',o.kind,'deleted',TRUE,'path',o.path,'note_created',o.note_created,'source_ref',o.source_ref,'snapshot','{}'::jsonb,'warnings','[]'::jsonb)
 FROM cleared_objects o WHERE ch.object_id=o.id RETURNING ch.cursor
), fresh_changes AS (
 INSERT INTO sync_changes(connection_id,object_id,revision,payload)
 SELECT o.connection_id,o.id,o.revision,jsonb_build_object('sync_id',o.id,'task_id',o.paca_task_id,'revision',o.revision,'kind',o.kind,'deleted',TRUE,'path',o.path,'note_created',o.note_created,'source_ref',o.source_ref,'snapshot','{}'::jsonb,'warnings','[]'::jsonb) FROM cleared_objects o RETURNING cursor
), cleared_operations AS (
 UPDATE sync_operations x SET body='{}',error='',lease_until=NULL,state=CASE WHEN x.state='applied' THEN 'applied' ELSE 'superseded' END,result=CASE WHEN x.state='applied' THEN x.result ELSE '{"discarded":true}'::jsonb END
 WHERE x.id IN(SELECT z.id FROM scoped_operations z WHERE z.object_id IN(SELECT id FROM cleared_objects) OR z.inbox_id IN(SELECT id FROM cleared_inbox)) RETURNING x.id
), cleared_conflicts AS (
 UPDATE sync_conflicts x SET base='{}',local='{}',remote='{}',fields='[]',state='cleared' WHERE x.object_id IN(SELECT id FROM cleared_objects) OR x.operation_id IN(SELECT id FROM cleared_operations) RETURNING x.id
), cleared_receipts AS (
 UPDATE sync_receipts x SET expected='{}',actual=NULL,fields='[]',state=CASE WHEN x.state='confirmed' THEN x.state ELSE 'cancelled' END
 WHERE x.object_id IN(SELECT id FROM cleared_objects) OR EXISTS(SELECT 1 FROM scoped_operations z WHERE z.connection_id=x.connection_id AND z.op_id=x.op_id AND z.inbox_id IN(SELECT id FROM cleared_inbox)) RETURNING x.id
)
INSERT INTO history_cleanups(id,connection_id,cutoff,deleted_tasks,processed_events,obsolete_sources)
SELECT $6,id,cutoff,(SELECT COUNT(*) FROM cleared_objects),(SELECT COUNT(*) FROM cleared_inbox),(SELECT COUNT(*) FROM cleared_sources) FROM scope`, req.PathParam("projectId"), req.PathParam("id"), body.Cutoff, body.Mode, string(ids), id)
	if err != nil { res.Error(503, "旧历史暂时无法清理，请重试。"); return }
	if n != 1 {
		rows, e := p.db.Query("SELECT id FROM connections WHERE id=$1 AND project_id=$2",req.PathParam("id"),req.PathParam("projectId"))
		if e==nil && len(rows.Rows)==0 { res.Error(404,"来源连接未找到。") } else { res.Error(409,"同步正在处理这个连接，请稍后重新核对清理范围。") }
		return
	}
	rows, err := p.db.Query("SELECT h.deleted_tasks,h.processed_events,h.obsolete_sources FROM history_cleanups h JOIN connections c ON c.id=h.connection_id WHERE h.id=$1 AND c.id=$2 AND c.project_id=$3", id, req.PathParam("id"), req.PathParam("projectId"))
	if err != nil || len(rows.Rows) != 1 { res.Error(503, "清理结果暂时无法读取，请刷新记录。"); return }
	r := rows.Rows[0]
	p.audit(req, "history.cleared", req.PathParam("id")+":"+fmt.Sprint(r))
	res.JSON(200, map[string]any{"deleted_tasks": r[0], "processed_events": r[1], "obsolete_sources": r[2]})
}
