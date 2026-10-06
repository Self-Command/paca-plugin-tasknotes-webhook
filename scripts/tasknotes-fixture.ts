// Run only in Actions. Exercise the pinned official controller with an Obsidian transport mock.
import { mkdir, writeFile } from "node:fs/promises";

const sha="69535cd956d11474b980deef8429ff0be232185f";
const response=await fetch(`https://raw.githubusercontent.com/callumalpass/tasknotes/${sha}/src/api/WebhookController.ts`);
if(!response.ok)throw new Error(`Official source unavailable: ${response.status}`);
const source=(await response.text()).replace(/^import .*;\r?\n/gm, "");
const prefix=`
class BaseController {}
const Get=()=>()=>{}; const Post=()=>()=>{}; const Delete=()=>()=>{};
const createTaskNotesLogger=()=>({error(){},warn(){}});
const requestUrl=globalThis.__capture;
`;
const captured: Array<{body:string;headers:Record<string,string>}>=[];
(globalThis as any).__capture=async(request:any)=>{captured.push({body:request.body,headers:request.headers});return {status:200,text:"OK"}};
const transpiler=new Bun.Transpiler({loader:"ts",tsconfig:{compilerOptions:{experimentalDecorators:true}}});
const code=transpiler.transformSync(prefix+source);
const {WebhookController}=await import(`data:text/javascript;base64,${Buffer.from(code).toString("base64")}`);
const secret="official-controller-ci-only-secret";
const plugin={settings:{webhooks:[{id:"fixture",url:"https://fixture.invalid",active:true,secret,events:["task.created","task.updated","task.deleted","task.archived","task.unarchived","task.completed"],successCount:0,failureCount:0}]},app:{vault:{getName:()=>"Official fixture vault",adapter:{basePath:"/ci/vault"}}},saveSettings:async()=>{}};
const controller=new WebhookController(plugin);
const initial={id:"Tasks/Integration.md",path:"Tasks/Integration.md",title:"官方 TaskNotes 任务",status:"open",priority:"high",scheduled:"2026-10-10T09:00:00+08:00",due:"2026-10-10T10:00:00+08:00",tags:["学习"],archived:false,dateModified:new Date(Date.now()-10000).toISOString()};
for(const [event,data] of [
 ["task.created",{task:initial}],
 ["task.updated",{task:{...initial,title:"官方任务已修改",priority:"low",dateModified:new Date(Date.now()-5000).toISOString()},previous:initial}],
 ["task.deleted",{task:{...initial,dateModified:new Date().toISOString()}}],
] as const){
 const before=captured.length;await controller.processWebhookTrigger(event,data);
 for(let i=0;captured.length===before&&i<100;i++)await Bun.sleep(10);
 if(captured.length!==before+1)throw new Error("Official controller did not emit delivery");
}
await mkdir("verification",{recursive:true});
await writeFile("verification/tasknotes-official-fixtures.json",JSON.stringify({source_sha:sha,secret,deliveries:captured},null,2));
