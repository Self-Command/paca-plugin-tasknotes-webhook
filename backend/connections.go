package main

import (
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "regexp"
 "strings"
 "time"

 plugin "github.com/Paca-AI/plugin-sdk-go"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
)

var uuidPattern=regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
type connectionInput struct {
 Name string `json:"name"`
 Timezone string `json:"timezone"`
 Enabled bool `json:"enabled"`
 StatusMap map[string]string `json:"status_map"`
 PriorityMap map[string]int `json:"priority_map"`
 Revision int `json:"revision"`
}
func validateConnection(c *connectionInput) bool {
 if c.Timezone=="" {c.Timezone="Asia/Shanghai"}
 if _,err:=time.LoadLocation(c.Timezone);err!=nil {return false}
 if strings.TrimSpace(c.Name)==""||len(c.Name)>120{return false}
 if c.StatusMap==nil {c.StatusMap=map[string]string{}}
 for k,v:=range c.StatusMap {if k==""||!uuidPattern.MatchString(v){return false}}
 if c.PriorityMap==nil {c.PriorityMap=map[string]int{"low":10,"normal":35,"high":75}}
 for _,v:=range c.PriorityMap {if v<0||v>1000{return false}}
 return true
}
func newUUID() (string,error) {
 b:=make([]byte,16);if _,err:=rand.Read(b);err!=nil{return "",err};b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128
 return fmt.Sprintf("%x-%x-%x-%x-%x",b[:4],b[4:6],b[6:8],b[8:10],b[10:]),nil
}
func (p *integrationPlugin) audit(req *plugin.Request, action,subject string) {
 _,_=p.db.Exec("INSERT INTO audit_log(project_id,actor_id,action,subject) VALUES($1,$2,$3,$4)",req.PathParam("projectId"),req.Caller.UserID,action,subject)
}
func (p *integrationPlugin) connections(req *plugin.Request,res *plugin.Response) {
 rows,err:=p.db.Query("SELECT id::text,name,enabled,timezone,status_map::text,priority_map::text,revision FROM connections WHERE project_id=$1 ORDER BY created_at",req.PathParam("projectId"))
 if err!=nil {res.Error(503,"connections unavailable");return}
 items:=[]any{}
 for _,r:=range rows.Rows {
  var statuses,priorities any;_=json.Unmarshal([]byte(fmt.Sprint(r[4])),&statuses);_=json.Unmarshal([]byte(fmt.Sprint(r[5])),&priorities)
  items=append(items,map[string]any{"id":r[0],"name":r[1],"enabled":r[2],"timezone":r[3],"status_map":statuses,"priority_map":priorities,"revision":r[6]})
 }
 res.JSON(200,map[string]any{"items":items})
}
func (p *integrationPlugin) createConnection(req *plugin.Request,res *plugin.Response) {
 c,err:=plugin.JSONBody[connectionInput](req);if err!=nil||!validateConnection(&c){res.Error(400,"invalid name, timezone or field mapping");return}
 id,err:=newUUID();if err!=nil{res.Error(503,"randomness unavailable");return}
 key:=make([]byte,32);if _,err=rand.Read(key);err!=nil{res.Error(503,"randomness unavailable");return}
 secret:=hex.EncodeToString(key);cipher,err:=p.encrypt(secret);if err!=nil{res.Error(503,"encryption unavailable");return}
 sm,_:=json.Marshal(c.StatusMap);pm,_:=json.Marshal(c.PriorityMap)
 _,err=p.db.Exec("INSERT INTO connections(id,project_id,name,secret_enc,enabled,timezone,status_map,priority_map) VALUES($1,$2,$3,$4,TRUE,$5,$6::jsonb,$7::jsonb)",id,req.PathParam("projectId"),c.Name,cipher,c.Timezone,string(sm),string(pm))
 if err!=nil{res.Error(503,"connection persistence failed");return}
 p.audit(req,"connection.created",id)
 base,_:=p.cfg.Get("PUBLIC_URL")
 res.JSON(201,map[string]any{"id":id,"revision":1,"secret":secret,"receive_url":strings.TrimRight(base,"/")+"/api/v1/plugins/"+pluginID+"/receive/"+id})
}
func (p *integrationPlugin) updateConnection(req *plugin.Request,res *plugin.Response) {
 c,err:=plugin.JSONBody[connectionInput](req);if err!=nil||!validateConnection(&c)||c.Revision<1{res.Error(400,"complete configuration and base revision required");return}
 sm,_:=json.Marshal(c.StatusMap);pm,_:=json.Marshal(c.PriorityMap)
 n,err:=p.db.Exec("UPDATE connections SET name=$1,enabled=$2,timezone=$3,status_map=$4::jsonb,priority_map=$5::jsonb,revision=revision+1 WHERE id=$6 AND project_id=$7 AND revision=$8",c.Name,c.Enabled,c.Timezone,string(sm),string(pm),req.PathParam("id"),req.PathParam("projectId"),c.Revision)
 if err!=nil{res.Error(503,"connection update failed");return};if n!=1{res.Error(409,"configuration changed; reload before saving");return}
 p.audit(req,"connection.updated",req.PathParam("id"));res.JSON(200,map[string]any{"revision":c.Revision+1})
}
func (p *integrationPlugin) rotateConnection(req *plugin.Request,res *plugin.Response) {
 key:=make([]byte,32);if _,err:=rand.Read(key);err!=nil{res.Error(503,"randomness unavailable");return}
 secret:=hex.EncodeToString(key);cipher,err:=p.encrypt(secret);if err!=nil{res.Error(503,"encryption unavailable");return}
 n,err:=p.db.Exec("UPDATE connections SET secret_enc=$1,revision=revision+1 WHERE id=$2 AND project_id=$3",cipher,req.PathParam("id"),req.PathParam("projectId"))
 if err!=nil{res.Error(503,"rotation failed");return};if n!=1{res.Error(404,"connection not found");return}
 p.audit(req,"connection.secret_rotated",req.PathParam("id"));res.JSON(200,map[string]any{"secret":secret})
}
func (p *integrationPlugin) receive(req *plugin.Request,res *plugin.Response) {
 id:=req.PathParam("id");if !uuidPattern.MatchString(id){res.Error(404,"connection not found");return}
 rows,err:=p.db.Query("SELECT secret_enc,enabled FROM connections WHERE id=$1",id)
 if err!=nil{res.Error(503,"receiver unavailable");return};if len(rows.Rows)!=1{res.Error(404,"connection not found");return}
 if rows.Rows[0][1]!=true{res.Error(409,"connection disabled");return}
 secret,err:=p.decrypt(fmt.Sprint(rows.Rows[0][0]));if err!=nil{res.Error(503,"receiver credential unavailable");return}
 if !tasknotes.ValidSignature(req.Body,secret,requestHeader(req,"X-TaskNotes-Signature")){res.Error(401,"invalid webhook signature");return}
 e,err:=tasknotes.Decode(req.Body);delivery:=requestHeader(req,"X-TaskNotes-Delivery-ID")
 if err!=nil||delivery==""||len(delivery)>200||requestHeader(req,"X-TaskNotes-Event")!=e.Event{res.Error(400,"invalid TaskNotes event or delivery ID");return}
 hash:=tasknotes.Hash(req.Body)
 n,err:=p.db.Exec("INSERT INTO inbox(connection_id,delivery_id,body,body_hash,event) VALUES($1,$2,$3::jsonb,$4,$5) ON CONFLICT(connection_id,delivery_id) DO NOTHING",id,delivery,string(req.Body),hash,e.Event)
 if err!=nil{res.Error(503,"event persistence failed; retry delivery");return}
 existing,err:=p.db.Query("SELECT id,state,body_hash FROM inbox WHERE connection_id=$1 AND delivery_id=$2",id,delivery)
 if err!=nil||len(existing.Rows)!=1{res.Error(503,"receipt unavailable");return}
 r:=existing.Rows[0];if r[2]!=hash{res.Error(409,"delivery ID reused with different content");return}
 code:=202;if n==0{code=200};res.JSON(code,map[string]any{"id":r[0],"state":r[1],"duplicate":n==0})
}
func (p *integrationPlugin) deliveries(req *plugin.Request,res *plugin.Response) {
 rows,err:=p.db.Query("SELECT i.id,i.delivery_id,i.event,i.state,i.error,i.attempts,i.received_at::text FROM inbox i JOIN connections c ON c.id=i.connection_id WHERE c.project_id=$1 AND c.id=$2 ORDER BY i.id DESC LIMIT 100",req.PathParam("projectId"),req.PathParam("id"))
 if err!=nil{res.Error(503,"history unavailable");return}
 items:=[]any{};for _,r:=range rows.Rows{items=append(items,map[string]any{"id":r[0],"delivery_id":r[1],"event":r[2],"state":r[3],"error":r[4],"attempts":r[5],"received_at":r[6]})}
 res.JSON(200,map[string]any{"items":items})
}
func (p *integrationPlugin) reprocess(req *plugin.Request,res *plugin.Response) {
 n,err:=p.db.Exec("UPDATE inbox SET state='pending',next_attempt=NOW(),updated_at=NOW() WHERE connection_id=$1 AND delivery_id=$2 AND state IN ('error','conflict','uncertain') AND EXISTS(SELECT 1 FROM connections c WHERE c.id=inbox.connection_id AND c.project_id=$3)",req.PathParam("id"),req.PathParam("deliveryId"),req.PathParam("projectId"))
 if err!=nil{res.Error(503,"reprocess failed");return};if n!=1{res.Error(409,"record missing or not eligible; applied events cannot be replayed");return}
 p.audit(req,"delivery.reprocess",req.PathParam("deliveryId"));res.JSON(200,map[string]any{"queued":true})
}
func (p *integrationPlugin) sources(req *plugin.Request,res *plugin.Response) {
 rows,err:=p.db.Query("SELECT s.id,s.source_key,COALESCE(s.paca_task_id::text,''),s.state,s.external_ref FROM sources s JOIN connections c ON c.id=s.connection_id WHERE c.id=$1 AND c.project_id=$2 ORDER BY s.id DESC LIMIT 100",req.PathParam("id"),req.PathParam("projectId"))
 if err!=nil{res.Error(503,"source associations unavailable");return}
 items:=[]any{};for _,r:=range rows.Rows{items=append(items,map[string]any{"id":r[0],"path":r[1],"task_id":r[2],"state":r[3],"external_ref":r[4]})};res.JSON(200,map[string]any{"items":items})
}
func (p *integrationPlugin) link(req *plugin.Request,res *plugin.Response) {
 body,err:=plugin.JSONBody[struct{SourceID int64 `json:"source_id"`;TaskID string `json:"task_id"`}](req)
 if err!=nil||body.SourceID<1||!uuidPattern.MatchString(body.TaskID){res.Error(400,"source_id and task_id required");return}
 // The worker verifies the target through the project-scoped core REST API before adopting it.
 n,err:=p.db.Exec("UPDATE sources SET paca_task_id=$1,state='link_pending',updated_at=NOW() WHERE id=$2 AND connection_id=$3 AND state<>'deleted' AND EXISTS(SELECT 1 FROM connections c WHERE c.id=sources.connection_id AND c.project_id=$4)",body.TaskID,body.SourceID,req.PathParam("id"),req.PathParam("projectId"))
 if err!=nil{res.Error(503,"link failed");return};if n!=1{res.Error(409,"source missing or tombstoned");return}
 p.audit(req,"source.link_requested",fmt.Sprint(body.SourceID));res.JSON(202,map[string]any{"pending_verification":true})
}
