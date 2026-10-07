import { useCallback, useEffect, useState } from "react";

const ID = "com.selfcommand.tasknotes-webhook";
type Connection = {id:string;name:string;enabled:boolean;timezone:string;status_map:Record<string,string>;priority_map:Record<string,number>;revision:number};
type Delivery = {id:number;delivery_id:string;event:string;state:string;error:string;attempts:number;received_at:string};
type Source = {id:number;path:string;task_id:string;state:string;external_ref:string};
const deliveryLabels:Record<string,string>={pending:"待处理",applied:"已同步",stale:"旧事件",conflict:"待解决冲突",error:"处理失败",uncertain:"创建待核对"};
const inputClass = "w-full rounded-md border border-input bg-background px-3 py-2 text-sm";
const buttonClass = "rounded-md border border-input bg-background px-3 py-2 text-sm hover:bg-accent disabled:opacity-50";
async function api<T>(path:string,method="GET",body?:unknown):Promise<T> {
  const r=await fetch(`/api/v1/plugins/${ID}${path}`,{method,credentials:"include",headers:{"Content-Type":"application/json"},body:body===undefined?undefined:JSON.stringify(body)});
  const data=await r.json();if(!r.ok) throw new Error(typeof data.error==="string"?data.error:data.error?.message??data.message??`HTTP ${r.status}`);return data;
}
export default function SettingsTab({projectId,canEdit=true}: {projectId:string;canEdit?:boolean}) {
  const base=`/projects/${encodeURIComponent(projectId)}`;
  const [connections,setConnections]=useState<Connection[]>([]);
  const [selected,setSelected]=useState<Connection|null>(null);
  const [deliveries,setDeliveries]=useState<Delivery[]>([]);
  const [sources,setSources]=useState<Source[]>([]);
  const [name,setName]=useState("TaskNotes 桌面端");
  const [timezone,setTimezone]=useState("Asia/Shanghai");
  const [statusMap,setStatusMap]=useState("{}");
  const [priorityMap,setPriorityMap]=useState('{"low":10,"normal":35,"high":75}');
  const [enabled,setEnabled]=useState(true);
  const [senderSecret,setSenderSecret]=useState("");
  const [state,setState]=useState("正在读取插件状态…");
  const [error,setError]=useState("");
  const [busy,setBusy]=useState(false);
  const [credential,setCredential]=useState<{secret:string;receive_url?:string}|null>(null);
  const [sourceID,setSourceID]=useState("");
  const [taskID,setTaskID]=useState("");
  const load=useCallback(async()=>{
    const result=await api<{items:Connection[]}>(`${base}/connections`);setConnections(result.items);setState("已连接宿主；TaskNotes → Paca 单向同步。");
  },[base]);
  const history=useCallback(async(id:string)=>{
    const [d,s]=await Promise.all([api<{items:Delivery[]}>(`${base}/connections/${id}/deliveries`),api<{items:Source[]}>(`${base}/connections/${id}/sources`)]);
    setDeliveries(d.items);setSources(s.items);
  },[base]);
  useEffect(()=>{let active=true;load().catch(e=>{if(active)setError(e.message)});return()=>{active=false;setCredential(null)}},[load]);
  useEffect(()=>{if(!selected)return;history(selected.id).catch(e=>setError(e.message));const timer=setInterval(()=>history(selected.id).catch(e=>setError(e.message)),10000);return()=>clearInterval(timer)},[selected,history]);
  const act=async(fn:()=>Promise<void>)=>{setBusy(true);setError("");try{await fn()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setBusy(false)}};
  function choose(c:Connection|null){setSelected(c);setCredential(null);setSenderSecret("");setName(c?.name??"TaskNotes 桌面端");setTimezone(c?.timezone??"Asia/Shanghai");setEnabled(c?.enabled??true);setStatusMap(JSON.stringify(c?.status_map??{},null,2));setPriorityMap(JSON.stringify(c?.priority_map??{low:10,normal:35,high:75},null,2))}
  function config(){return {name,timezone,enabled,status_map:JSON.parse(statusMap),priority_map:JSON.parse(priorityMap),revision:selected?.revision,secret:senderSecret.trim()||undefined}}
  return <section className="space-y-5 text-foreground">
    <div><h2 className="text-lg font-semibold">TaskNotes 接入</h2><p role="status" className="text-sm text-muted-foreground">{state}</p></div>
    {error&&<p role="alert" className="rounded-lg border border-destructive p-3 text-sm text-destructive">{error}</p>}
    <div className="rounded-xl border bg-card p-5 space-y-3">
      <h3 className="font-medium">官方 TaskNotes 配置</h3>
      <p className="text-sm text-muted-foreground">先创建项目连接，复制接收地址到官方桌面 TaskNotes 的 Webhooks。TaskNotes 界面会自动生成 Secret，请复制回来填入下面并保存。订阅 task.created、task.updated、task.completed、task.deleted、task.archived、task.unarchived。移动端与未送达离线事件补齐暂不支持。</p>
      <label className="block text-sm">项目连接<select aria-label="项目连接" className={inputClass} value={selected?.id??""} onChange={e=>choose(connections.find(c=>c.id===e.target.value)??null)}><option value="">新建连接</option>{connections.map(c=><option key={c.id} value={c.id}>{c.name}{c.enabled?"":"（停用）"}</option>)}</select></label>
      <div className="grid gap-3 sm:grid-cols-2"><label className="text-sm">连接名称<input className={inputClass} value={name} onChange={e=>setName(e.target.value)}/></label><label className="text-sm">时区<input className={inputClass} value={timezone} onChange={e=>setTimezone(e.target.value)}/></label></div>
      {selected&&<><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>启用接收和导入</label><p className="break-all rounded-md bg-muted p-3 text-sm">{location.origin}/api/v1/plugins/{ID}/receive/{selected.id}</p></>}
      <label className="block text-sm">TaskNotes 生成的 Secret<input aria-label="TaskNotes 生成的 Secret" type="password" autoComplete="new-password" spellCheck={false} className={inputClass} value={senderSecret} onChange={e=>setSenderSecret(e.target.value)} placeholder={selected?"粘贴新的 Secret；留空保留已保存的值":"可先留空创建连接，获得地址后再回来保存 Secret"}/></label>
      <p className="text-xs text-muted-foreground">Secret 加密保存，不会重新显示；保存 Secret 不会重建任务或清除接收历史。</p>
      <div className="grid gap-3 sm:grid-cols-2"><label className="text-sm">状态映射（状态 → Paca 状态 UUID）<textarea rows={4} className={inputClass} value={statusMap} onChange={e=>setStatusMap(e.target.value)}/></label><label className="text-sm">优先级映射<textarea rows={4} className={inputClass} value={priorityMap} onChange={e=>setPriorityMap(e.target.value)}/></label></div>
      <p className="text-xs text-muted-foreground">默认 open / none → todo，in-progress → inprogress，完成 → done；自定义状态需填写映射；@archived 必须指向项目的归档状态，@completed 可指定完成状态。仅日期保留来源信息，不自动安排午夜提醒。</p>
      <div className="flex flex-wrap gap-2"><button className={buttonClass} disabled={busy||!canEdit} onClick={()=>act(async()=>{
        if(selected){const saved=await api<{revision:number}>(`${base}/connections/${selected.id}`,"PATCH",config());choose({...selected,name,timezone,enabled,status_map:JSON.parse(statusMap),priority_map:JSON.parse(priorityMap),revision:saved.revision})}
        else {const created=await api<{id:string;revision:number;secret:string;receive_url:string}>(`${base}/connections`,"POST",config());const imported=senderSecret.trim()!=="";choose({id:created.id,revision:created.revision,name,timezone,enabled:true,status_map:JSON.parse(statusMap),priority_map:JSON.parse(priorityMap)});if(!imported)setCredential(created)}
        await load();
      })}>{selected?"保存连接":"创建连接"}</button>{selected&&<button className={buttonClass} disabled={busy||!canEdit} onClick={()=>act(async()=>{const rotated=await api<{secret:string}>(`${base}/connections/${selected.id}/rotate-secret`,"POST",{});const result=await api<{items:Connection[]}>(`${base}/connections`);setConnections(result.items);choose(result.items.find(c=>c.id===selected.id)??null);setCredential(rotated)})}>生成 Secret（用于 TaskNotes API）</button>}<button className={buttonClass} disabled={busy} onClick={()=>act(async()=>{await load();if(selected)await history(selected.id)})}>刷新</button></div>
      {credential&&<div className="rounded-lg border border-primary p-3 space-y-2"><p className="text-sm font-medium">仅用于通过 TaskNotes API 创建 Webhook；官方设置界面请使用 TaskNotes 自动生成的 Secret，回到上方粘贴并保存。不要提交 Secret 到仓库。</p>{credential.receive_url&&<p className="break-all text-sm">{credential.receive_url}</p>}<code className="block break-all select-all text-sm">{credential.secret}</code><button className={buttonClass} onClick={()=>setCredential(null)}>隐藏</button></div>}
    </div>
    {selected&&<><div className="rounded-xl border bg-card p-5 space-y-3"><h3 className="font-medium">接收历史（最近 100 条）</h3>{deliveries.length===0?<p className="text-sm text-muted-foreground">尚未接收事件。</p>:<div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>事件</th><th>状态</th><th>接收时间</th><th>处理结果</th></tr></thead><tbody>{deliveries.map(d=><tr key={d.id} className="border-t"><td className="py-3">{d.event}<br/><span className="text-xs text-muted-foreground">{d.delivery_id}</span></td><td>{deliveryLabels[d.state]??d.state}</td><td>{d.received_at}</td><td>{d.error}{["error","conflict","uncertain"].includes(d.state)&&<button className={buttonClass} disabled={busy||!canEdit} onClick={()=>act(async()=>{await api(`${base}/connections/${selected.id}/reprocess/${encodeURIComponent(d.delivery_id)}`,"POST",{});await history(selected.id)})}>重新核对</button>}</td></tr>)}</tbody></table></div>}</div>
      <div className="rounded-xl border bg-card p-5 space-y-3"><h3 className="font-medium">来源关联与创建结果核对</h3><p className="text-sm text-muted-foreground">创建结果不明时，先在 Paca 查询来源标记。关联到确认存在的任务后，再重新核对对应事件。删除墓碑不会自动复活。</p>
      {sources.map(s=><div key={s.id} className="border-b pb-2 text-sm"><strong>#{s.id} {s.path}</strong><span className="ml-2">{s.state}</span>{s.task_id&&<a className="ml-2 text-primary underline" href={`/projects/${projectId}/tasks/${s.task_id}`}>打开任务</a>}<code className="block break-all text-xs text-muted-foreground">{s.external_ref}</code></div>)}
      <div className="grid gap-2 sm:grid-cols-2"><input aria-label="来源 ID" placeholder="来源 ID" className={inputClass} value={sourceID} onChange={e=>setSourceID(e.target.value)}/><input aria-label="Paca 任务 UUID" placeholder="Paca 任务 UUID" className={inputClass} value={taskID} onChange={e=>setTaskID(e.target.value)}/></div>
      <button className={buttonClass} disabled={busy||!canEdit} onClick={()=>act(async()=>{await api(`${base}/connections/${selected.id}/link`,"POST",{source_id:Number(sourceID),task_id:taskID});await history(selected.id)})}>提交关联并由 worker 验证</button></div></>}
  </section>;
}
