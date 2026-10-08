package worker

import (
 "context"
 "encoding/json"
 "errors"
 "regexp"
 "strconv"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
 "github.com/jackc/pgx/v5"
)

func snapshotTask(t tasknotes.Task)tasksync.Snapshot{
 raw,_:=json.Marshal(t);s:=tasksync.Snapshot{};_=json.Unmarshal(raw,&s);out:=tasksync.Snapshot{};for field:=range tasksync.Fields{out[field]=s[field]};if text,ok:=out["details"].(string);ok{out["details"]=tasksync.TaskBody(text)};for _,f:=range []string{"scheduled","due"}{if out[f]==""{out[f]=nil}};if out["recurrence"]==""{out["recurrence"]=nil};return out
}
func (w *Worker) syncWebhook(ctx context.Context,c connection,s source,e tasknotes.Envelope,inbox int64)(bool,error){
 config,err:=w.syncConfig(ctx,c.ID);if err!=nil{return false,err};if config.Mode!="enabled"||s.TaskID==""{return false,nil}
 var native nativeTask;err=w.call(ctx,"GET","/projects/"+c.Project+"/tasks/"+s.TaskID,nil,&native);var api apiError;if errors.As(err,&api)&&api.Code==404&&e.Event=="task.deleted"{return true,w.applied(ctx,inbox,s,e,"deleted")};if err!=nil{return true,err};if err=w.acceptNative(ctx,config,native,false);err!=nil{return true,err}
 var object string;var revision int64;var remoteRaw []byte;err=w.Pool.QueryRow(ctx,"SELECT id::text,revision,snapshot FROM sync_objects WHERE connection_id=$1 AND paca_task_id=$2",c.ID,s.TaskID).Scan(&object,&revision,&remoteRaw);if err!=nil{return true,err}
 incoming:=snapshotTask(e.EffectiveTask());var remote tasksync.Snapshot;_=json.Unmarshal(remoteRaw,&remote)
 // Explicit receipts identify every server-origin write, not only check-in status.
 if len(tasksync.Diff(incoming,remote))==0{return true,w.applied(ctx,inbox,s,e,s.State)}
 rows,err:=w.Pool.Query(ctx,"SELECT COALESCE(actual,expected) FROM sync_receipts WHERE object_id=$1 AND state IN('prepared','confirmed','observed') ORDER BY created_at DESC LIMIT 20",object);if err!=nil{return true,err};matched:=false;for rows.Next(){var raw []byte;if rows.Scan(&raw)!=nil{rows.Close();return true,errors.New("receipt unavailable")};var expected tasksync.Snapshot;_=json.Unmarshal(raw,&expected);if len(tasksync.Diff(expected,incoming))==0{matched=true}};rows.Close();if matched{return true,w.applied(ctx,inbox,s,e,s.State)}
 if s.Snapshot==nil&&e.Data.Previous==nil{return true,w.finish(ctx,inbox,"conflict","来源快照缺失，请先确认任务关联。")}
 base:=tasksync.Snapshot{};if s.Snapshot!=nil{base=snapshotTask(*s.Snapshot)};if e.Data.Previous!=nil{base=snapshotTask(*e.Data.Previous)};changes:=tasksync.Diff(base,incoming)
 kind:="update";if e.Event=="task.deleted"{kind="delete"};if len(changes)==0&&kind!="delete"{return true,w.applied(ctx,inbox,s,e,s.State)}
 op:=tasksync.Operation{OpID:"webhook:"+tasksync.ID(c.ID,strconv.FormatInt(inbox,10)+e.Timestamp),SyncID:object,BaseRevision:revision,Kind:kind,Base:base,Changes:changes,Path:e.Data.Task.Path,Created:e.Data.Task.DateCreated};raw,_:=json.Marshal(op)
 _,err=w.Pool.Exec(ctx,"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body,inbox_id) VALUES($1,$2,'webhook',$3,$4,$5::jsonb,$6) ON CONFLICT(connection_id,op_id) DO NOTHING",c.ID,object,op.OpID,tasksync.Hash(op),string(raw),inbox);if err!=nil{return true,err}
 return true,w.finish(ctx,inbox,"pending_sync","任务变更已进入双向同步队列。")
}
func (w *Worker) markerSource(ctx context.Context,c connection,e tasknotes.Envelope)(bool,error){
 var task,ref,state string;var err error
 if e.Data.Task.Details!=nil {m:=regexp.MustCompile(`<!-- paca-sync-id:([a-fA-F0-9-]{36}) -->`).FindStringSubmatch(*e.Data.Task.Details);if len(m)==2{err=w.Pool.QueryRow(ctx,"SELECT COALESCE(paca_task_id::text,''),source_ref,binding_state FROM sync_objects WHERE id=$1 AND connection_id=$2 AND NOT deleted",m[1],c.ID).Scan(&task,&ref,&state)}}
 if task==""&&e.Data.Task.DateCreated!="" {err=w.Pool.QueryRow(ctx,"SELECT COALESCE(paca_task_id::text,''),source_ref,binding_state FROM sync_objects WHERE connection_id=$1 AND path=$2 AND note_created=$3 AND NOT deleted",c.ID,e.Data.Task.Path,e.Data.Task.DateCreated).Scan(&task,&ref,&state)}
 if errors.Is(err,pgx.ErrNoRows)||task==""{return false,nil};if err!=nil{return false,err}
 raw,_:=json.Marshal(e.Data.Task);var id int64;err=w.Pool.QueryRow(ctx,"INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot) VALUES($1,$2,$3,$4,$5,'linked',$6::jsonb) ON CONFLICT(external_ref) DO UPDATE SET paca_task_id=EXCLUDED.paca_task_id RETURNING id",c.ID,e.Vault.Path,e.Data.Task.Path,task,ref,string(raw)).Scan(&id);if err!=nil{return true,err}
 _,err=w.Pool.Exec(ctx,"INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,$2,$3,$4) ON CONFLICT(connection_id,vault_key,path) DO NOTHING",c.ID,e.Vault.Path,e.Data.Task.Path,id);return true,err
}
