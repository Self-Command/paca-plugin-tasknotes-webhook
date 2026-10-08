package worker

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "strings"
 "time"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
 "github.com/jackc/pgx/v5"
)

// Every period is a durable operation in the ordinary sync pipeline. It inherits
// the same lost-response recovery and idempotent source marker as a normal task.
func (w *Worker) queuePeriodOperation(ctx context.Context,c syncConfig,id string,revision int64,kind string,base,changes tasksync.Snapshot,identity string) error {
 op:=tasksync.Operation{OpID:"series:"+identity,SyncID:id,BaseRevision:revision,Kind:kind,Base:base,Changes:changes}
 raw,_:=json.Marshal(op)
 _,err:=w.Pool.Exec(ctx,"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,'recurrence',$3,$4,$5::jsonb) ON CONFLICT(connection_id,op_id) DO NOTHING",c.ID,id,op.OpID,tasksync.Hash(op),string(raw))
 return err
}
func normalizedPeriod(parent tasksync.Snapshot,p recurrencePeriod,series string) tasksync.Snapshot {
 out:=tasksync.Snapshot{}
 for field:=range tasksync.Fields {out[field]=p.Snapshot[field]}
 out["details"]=parent["details"];out["archived"]=false
 out["recurrence"]=nil;out["recurrence_anchor"]=nil;out["complete_instances"]=nil;out["skipped_instances"]=nil
 out["recurrence_parent"]=series;out["occurrence_date"]=p.Date
 return out
}
func (w *Worker) RecurrenceTick(ctx context.Context) error {
 if err:=w.control(ctx);err!=nil{return err}
 // A project has one enabled coordinator. Duplicate connections must be resolved
 // before scheduling; they must never produce two sets of tasks or reminders.
 rows,err:=w.Pool.Query(ctx,"SELECT o.id::text,c.id::text,o.snapshot,o.deleted FROM sync_objects o JOIN connections c ON c.id=o.connection_id LEFT JOIN recurring_series s ON s.object_id=o.id WHERE c.enabled AND c.sync_mode='enabled' AND c.recurrence_enabled AND o.canonical_id IS NULL AND (o.kind='series' OR s.object_id IS NOT NULL) AND (s.last_reconcile IS NULL OR s.last_reconcile<NOW()-INTERVAL '1 minute' OR o.updated_at>s.last_reconcile) ORDER BY s.last_reconcile NULLS FIRST LIMIT 1")
 if err!=nil{return err}
 var id,connectionID string;var raw []byte;var deleted bool
 if !rows.Next(){rows.Close();return nil}
 err=rows.Scan(&id,&connectionID,&raw,&deleted);rows.Close();if err!=nil{return err}
 c,err:=w.syncConfig(ctx,connectionID);if err!=nil{return err}
 var coordinators int
 if err=w.Pool.QueryRow(ctx,"SELECT COUNT(*) FROM connections WHERE project_id=$1 AND enabled AND sync_mode='enabled' AND recurrence_enabled",c.Project).Scan(&coordinators);err!=nil{return err}
 if coordinators!=1{return errors.New("同一项目只能启用一个循环排期来源。")}
 conn,err:=w.Pool.Acquire(ctx);if err!=nil{return err};defer conn.Release()
 var locked bool
 if err=conn.QueryRow(ctx,"SELECT pg_try_advisory_lock(hashtextextended($1,3))",id).Scan(&locked);err!=nil||!locked{return err}
 defer conn.Exec(context.Background(),"SELECT pg_advisory_unlock(hashtextextended($1,3))",id)
 var snapshot tasksync.Snapshot
 if json.Unmarshal(raw,&snapshot)!=nil{return errors.New("循环任务数据无效。")}
 if err=w.reconcileSeries(ctx,c,id,snapshot,deleted);err!=nil {
  _,_=w.Pool.Exec(ctx,"UPDATE recurring_series SET last_error=$2,last_reconcile=NOW() WHERE object_id=$1",id,err.Error())
  return err
 }
 _,err=w.Pool.Exec(ctx,"UPDATE recurring_series SET last_error='',last_reconcile=NOW() WHERE object_id=$1",id)
 return err
}
func (w *Worker) reconcileSeries(ctx context.Context,c syncConfig,id string,snapshot tasksync.Snapshot,deleted bool) error {
 loc,_:=time.LoadLocation(c.Timezone);if loc==nil{return errors.New("循环时区无效。")}
 now:=time.Now().In(loc);today:=now.Format("2006-01-02");end:=now.AddDate(0,0,30).Format("2006-01-02")
 state:="active"
 rule,_:=snapshot["recurrence"].(string)
 if deleted||rule==""{state="cancelled"}else if snapshot["archived"]==true{state="paused"}
 definition:=cloneSnapshot(snapshot)
 var previousRaw []byte;var oldHash,oldState string;var ruleRevision int64
 err:=w.Pool.QueryRow(ctx,"SELECT definition,rule_hash,rule_revision,state FROM recurring_series WHERE object_id=$1",id).Scan(&previousRaw,&oldHash,&ruleRevision,&oldState)
 if err!=nil&&!errors.Is(err,pgx.ErrNoRows){return err}
 previous:=tasksync.Snapshot{};_=json.Unmarshal(previousRaw,&previous)
 // Official completion moves scheduled and may add DTSTART. Preserve the fixed
 // calendar seed while recognizing that history advancement is not a rule edit.
 if snapshot["recurrence_anchor"]!="completion"&&len(previous)>0 {
  if tasksync.Hash(previous["complete_instances"])!=tasksync.Hash(snapshot["complete_instances"])||tasksync.Hash(previous["skipped_instances"])!=tasksync.Hash(snapshot["skipped_instances"])||tasksync.Equal(previous["__observed_scheduled"],snapshot["scheduled"]) {
   if ruleWithoutStart(fmt.Sprint(previous["recurrence"]))==ruleWithoutStart(rule){definition["scheduled"]=previous["scheduled"];definition["due"]=previous["due"];definition["recurrence"]=previous["recurrence"]}
  }
 }
 definition["__observed_scheduled"]=snapshot["scheduled"]
 hashFields:=cloneSnapshot(definition);delete(hashFields,"__observed_scheduled");delete(hashFields,"complete_instances");delete(hashFields,"skipped_instances");delete(hashFields,"status");delete(hashFields,"archived")
 hash:=tasksync.Hash(hashFields)
 if ruleRevision==0{ruleRevision=1}else if oldHash!=hash||oldState!=state{ruleRevision++}
 raw,_:=json.Marshal(definition)
 _,err=w.Pool.Exec(ctx,"INSERT INTO recurring_series(object_id,connection_id,rule_revision,rule_hash,definition,state) VALUES($1,$2,$3,$4,$5::jsonb,$6) ON CONFLICT(object_id) DO UPDATE SET rule_revision=EXCLUDED.rule_revision,rule_hash=EXCLUDED.rule_hash,definition=EXCLUDED.definition,state=EXCLUDED.state,updated_at=NOW()",id,c.ID,ruleRevision,hash,string(raw),state)
 if err!=nil{return err}
 if advanced,progressErr:=w.advanceCompletedPeriods(ctx,c,id,snapshot);progressErr!=nil{return progressErr}else if advanced{return nil}
 desired:=map[string]tasksync.Snapshot{}
 if state=="active" {
  output,modelErr:=recurrenceModel(ctx,c.Timezone,map[string]any{"task":definition,"today":today,"end":end,"now":now.Format(time.RFC3339),"series_id":id})
  if modelErr!=nil{return modelErr}
  for _,period:=range output.Periods{desired[period.Date]=normalizedPeriod(definition,period,id)}
 }
 // Reconcile existing future periods first. Past periods and tombstones are never recreated.
 rows,err:=w.Pool.Query(ctx,"SELECT p.occurrence_date::text,p.object_id::text,p.rule_revision,p.state,p.expected,o.snapshot,o.revision,COALESCE(o.paca_task_id::text,''),o.deleted FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.series_id=$1",id)
 if err!=nil{return err}
 type existing struct{date,id,state,task string;rule,revision int64;expected,current tasksync.Snapshot;deleted bool}
 periods:=[]existing{}
 for rows.Next(){var p existing;var expected,current []byte;if err=rows.Scan(&p.date,&p.id,&p.rule,&p.state,&expected,&current,&p.revision,&p.task,&p.deleted);err!=nil{rows.Close();return err};_=json.Unmarshal(expected,&p.expected);_=json.Unmarshal(current,&p.current);periods=append(periods,p)}
 err=rows.Err();rows.Close();if err!=nil{return err}
 for _,p:=range periods {
  candidate,present:=desired[p.date];delete(desired,p.date)
  if p.deleted&&p.state=="planned" {_,err=w.Pool.Exec(ctx,"UPDATE recurring_periods SET state='deleted',updated_at=NOW() WHERE object_id=$1",p.id);if err!=nil{return err};continue}
  if state=="paused"&&p.state=="planned" {candidate=cloneSnapshot(p.expected);candidate["archived"]=true;present=true}
  if (p.state!="planned"&&!(p.state=="cancelled"&&present&&!p.deleted))||p.date<today{continue}
  if present&&p.rule==ruleRevision&&tasksync.Equal(candidate,p.expected){continue}
  // Before changing a period, all earlier durable creates/updates must have settled.
  var pending bool
  if err=w.Pool.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM sync_operations WHERE object_id=$1 AND state IN('pending','retry','sending','uncertain','conflict'))",p.id).Scan(&pending);err!=nil{return err};if pending{continue}
  frozen,freezeErr:=w.periodFrozen(ctx,c,p.task);if freezeErr!=nil{return freezeErr}
  if frozen&&present&&state=="active"&&(!tasksync.Equal(candidate["scheduled"],p.expected["scheduled"])||!tasksync.Equal(candidate["due"],p.expected["due"])) {
   _,err=w.Pool.Exec(ctx,"UPDATE recurring_periods SET state='conflict',last_error='本期打卡窗口已开放，时间保持冻结，请取消后重新安排。',updated_at=NOW() WHERE object_id=$1",p.id);if err!=nil{return err};continue
  }
  kind:="update";changes:=tasksync.Diff(p.expected,candidate)
  if !present{candidate=cloneSnapshot(p.expected);candidate["archived"]=true;changes=tasksync.Diff(p.expected,candidate);if state=="cancelled"&&deleted{kind="delete";changes=tasksync.Snapshot{}}}
  if err=w.queuePeriodOperation(ctx,c,p.id,p.revision,kind,p.expected,changes,fmt.Sprintf("%s:%s:%d:%s",id,p.date,ruleRevision,state));err!=nil{return err}
  periodState:="planned";if !present{periodState="cancelled"}
  raw,_:=json.Marshal(candidate)
  _,err=w.Pool.Exec(ctx,"UPDATE recurring_periods SET rule_revision=$2,expected=$3::jsonb,state=$4,updated_at=NOW() WHERE object_id=$1",p.id,ruleRevision,string(raw),periodState);if err!=nil{return err}
 }
 for date,period:=range desired {
  periodID:=tasksync.ID(id,date)
  tx,err:=w.Pool.Begin(ctx);if err!=nil{return err}
  if _,err=tx.Exec(ctx,"INSERT INTO sync_objects(id,connection_id,source_ref,kind) VALUES($1,$2,$3,'occurrence') ON CONFLICT(id) DO NOTHING",periodID,c.ID,"period:"+id+":"+date);err!=nil{tx.Rollback(ctx);return err}
  raw,_:=json.Marshal(period)
  inserted,err:=tx.Exec(ctx,"INSERT INTO recurring_periods(series_id,occurrence_date,object_id,rule_revision,expected) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(series_id,occurrence_date) DO NOTHING",id,date,periodID,ruleRevision,string(raw))
  if err!=nil{tx.Rollback(ctx);return err}
  if inserted.RowsAffected()==1 {
   op:=tasksync.Operation{OpID:"period-create:"+periodID,SyncID:periodID,Kind:"create",Base:tasksync.Snapshot{},Changes:period}
   body,_:=json.Marshal(op)
   _,err=tx.Exec(ctx,"INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,'recurrence',$3,$4,$5::jsonb) ON CONFLICT(connection_id,op_id) DO NOTHING",c.ID,periodID,op.OpID,tasksync.Hash(op),string(body))
  }
  if err!=nil{tx.Rollback(ctx);return err};if err=tx.Commit(ctx);err!=nil{return err}
 }
 return nil
}
func cloneSnapshot(s tasksync.Snapshot) tasksync.Snapshot {out:=tasksync.Snapshot{};for k,v:=range s{out[k]=v};return out}
func ruleWithoutStart(rule string) string {if strings.HasPrefix(rule,"DTSTART:"){if i:=strings.Index(rule,";");i>=0{return rule[i+1:]}};return rule}
