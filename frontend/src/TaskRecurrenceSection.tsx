import {useEffect,useState} from "react";
import {Card,CardHeader,CardTitle,CardContent,CardDescription} from "./components/ui/card";
import {Button} from "./components/ui/button";
import {Input} from "./components/ui/input";
import {Select,SelectTrigger,SelectContent,SelectItem,SelectValue} from "./components/ui/select";
import "./theme.css";
type Plan={connection_id:string;revision:number;enabled:boolean;snapshot:{recurrence?:string;recurrence_anchor?:string;scheduled?:string;due?:string};periods:{date:string;state:string;error:string}[];error:string};
export default function TaskRecurrenceSection({projectId,taskId,canEdit=true}:{projectId:string;taskId:string;canEdit?:boolean}) {
 const [plans,setPlans]=useState<Plan[]>([]),[rule,setRule]=useState(""),[anchor,setAnchor]=useState("scheduled"),[expanded,setExpanded]=useState(false),[loaded,setLoaded]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const route=`/api/v1/plugins/com.selfcommand.tasknotes-webhook/projects/${projectId}/tasks/${taskId}/recurrence`;
 async function load(){const response=await fetch(route,{credentials:"include"});if(!response.ok)throw new Error("循环设置暂时无法读取。");const result=await response.json();const values=result.data?.items??result.items;setPlans(values);setLoaded(true);if(values.length===1){const s=values[0].snapshot;setRule(s.recurrence??"");setAnchor(s.recurrence_anchor??"scheduled");setExpanded(!!s.recurrence)}}
 useEffect(()=>{void load().catch(e=>setError(e.message))},[route]);
 async function save(){if(plans.length!==1)return;setBusy(true);setError("");try{const plan=plans[0];const response=await fetch(route,{method:"PUT",credentials:"include",headers:{"Content-Type":"application/json"},body:JSON.stringify({connection_id:plan.connection_id,revision:plan.revision,op_id:crypto.randomUUID(),recurrence:rule,recurrence_anchor:anchor})});if(!response.ok)throw new Error("循环设置未保存，请检查来源开关、规则和任务版本。");await load()}catch(e){setError(e instanceof Error?e.message:"暂时无法保存，请稍后重试。")}finally{setBusy(false)}}
 return <Card><CardHeader><CardTitle>重复计划</CardTitle><CardDescription>{!loaded?"正在读取任务类型…":plans[0]?.snapshot.recurrence?"此任务按规则重复，各期分别安排提醒。":"此任务为一次性任务，不会自动重复。"}</CardDescription></CardHeader><CardContent className="grid gap-3">
 {!loaded?<p className="text-sm text-muted-foreground">正在读取任务安排…</p>:plans.length!==1?<p className="text-sm text-muted-foreground">请先在项目来源设置中接入此任务，并保留一个循环排期来源。</p>:<>
 {!expanded?<Button variant="outline" disabled={!canEdit||busy||!plans[0].enabled} onClick={()=>setExpanded(true)}>设置重复计划</Button>:<>
 {!plans[0].enabled&&<p className="text-sm text-muted-foreground">请先在项目来源设置中启用循环排期。</p>}
 <label className="grid gap-1 text-sm">循环规则<Input value={rule} onChange={e=>setRule(e.target.value)} disabled={!canEdit||busy}/><span className="text-muted-foreground">支持每天、每周、每月和每年；留空可停止整个系列。</span></label>
 <div className="flex flex-wrap gap-2">{[["每天","FREQ=DAILY"],["每周","FREQ=WEEKLY"],["每月","FREQ=MONTHLY"],["每年","FREQ=YEARLY"]].map(([label,value])=><Button key={value} variant="outline" disabled={!canEdit||busy} onClick={()=>setRule(value)}>{label}</Button>)}</div>
 <label className="grid gap-1 text-sm">循环锚定<Select value={anchor} onValueChange={setAnchor}><SelectTrigger disabled={!canEdit||busy}><SelectValue/></SelectTrigger><SelectContent><SelectItem value="scheduled">按计划日期循环</SelectItem><SelectItem value="completion">完成本期后安排下一期</SelectItem></SelectContent></Select></label>
 <p className="text-sm text-muted-foreground">开始、结束时分和提前提醒请在下方<a className="text-primary underline" href="#task-times-reminders">“任务时间与提醒”</a>设置。这里仅修改重复规则。</p>
 <Button onClick={()=>void save()} disabled={!canEdit||busy||!plans[0].enabled}>{busy?"正在保存":"保存循环规则"}</Button>
 <Button variant="outline" onClick={()=>void load().catch(e=>setError(e.message))}>刷新周期清单</Button>
 </>}
 {plans[0].error&&<p role="alert" className="text-destructive text-sm">{plans[0].error}</p>}
 {plans[0].periods.map(period=><Card key={period.date}><CardContent className="py-3 text-sm"><strong>{period.date}</strong><p>{({planned:"已安排",completed:"已完成",skipped:"已跳过",deleted:"已删除",cancelled:"已取消",conflict:"需要核对"} as Record<string,string>)[period.state]??"需要核对"}</p>{period.error&&<p className="text-destructive">{period.error}</p>}</CardContent></Card>)}
 </>}{error&&<p role="alert" className="text-destructive text-sm">{error}</p>}
 </CardContent></Card>;
}
