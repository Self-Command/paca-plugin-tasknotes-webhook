import { useEffect, useState } from "react";

export default function SettingsTab({projectId}: {projectId: string; canEdit?: boolean}) {
  const [state,setState] = useState("正在读取插件状态…");
  useEffect(() => {
    const controller = new AbortController();
    fetch(`/api/v1/plugins/com.selfcommand.tasknotes-webhook/projects/${encodeURIComponent(projectId)}/status`,{credentials:"include",signal:controller.signal})
      .then(r => { if(!r.ok) throw new Error(`HTTP ${r.status}`); return r.json(); })
      .then(() => setState("已连接宿主；业务功能尚在开发，未启用后台处理。"))
      .catch(e => {if(e.name !== "AbortError") setState(`状态读取失败：${e.message}`);});
    return () => controller.abort();
  },[projectId]);
  return <section className="space-y-4"><h2 className="text-lg font-semibold">TaskNotes 接入</h2><div className="rounded-xl border bg-card p-5 text-card-foreground"><p role="status">{state}</p><p className="mt-2 text-sm text-muted-foreground">独立插件与独立 worker，保持 Paca 原有功能。</p></div></section>;
}
