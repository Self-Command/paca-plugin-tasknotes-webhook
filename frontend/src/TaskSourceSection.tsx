import {useEffect,useState} from "react";
type Source={source?:string;timezone?:string;start_instant?:string;due_instant?:string;start_source?:string;due_source?:string;archived?:boolean;recurring?:boolean};
function display(instant:string|undefined,day:string|undefined,zone:string|undefined){
 if(!instant)return day?`${day}（尚无准确时刻）`:"未设置";
 try{return new Intl.DateTimeFormat("zh-CN",{timeZone:zone||"Asia/Shanghai",dateStyle:"medium",timeStyle:"short"}).format(new Date(instant))}catch{return instant}
}
export default function TaskSourceSection({projectId,taskId}:{projectId:string;taskId:string}){
 const [source,setSource]=useState<Source|null>(null);
 useEffect(()=>{
  let active=true;
  const load=async()=>{const r=await fetch(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`,{credentials:"include"});if(!r.ok)return;const data=await r.json();const meta=data.data?.custom_fields?._integration_state_v1;if(active)setSource(meta?.source==="tasknotes"?meta:null)};
  load().catch(()=>{});const timer=setInterval(()=>load().catch(()=>{}),10000);
  return()=>{active=false;clearInterval(timer)};
 },[projectId,taskId]);
 if(!source)return null;
 return <section className="rounded-xl border bg-card p-4 space-y-2">
  <h3 className="font-medium">TaskNotes 来源</h3>
  <p className="text-sm text-muted-foreground">标题、时间、状态、优先级和来源标签由 TaskNotes 管理；下一次成功同步会更新这些字段。Paca 改动不会写回 Obsidian。</p>
  <dl className="grid gap-2 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">开始</dt><dd>{display(source.start_instant,source.start_source,source.timezone)}</dd></div><div><dt className="text-muted-foreground">截止</dt><dd>{display(source.due_instant,source.due_source,source.timezone)}</dd></div></dl>
  {source.archived&&<p className="text-sm">来源已归档，本插件保留 Paca 任务。</p>}
  {source.recurring&&<p className="text-sm">循环任务已导入；首版推送队列不展开循环排期。</p>}
 </section>;
}
