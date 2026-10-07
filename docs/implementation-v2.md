# v2.0 六事件同步实施记录

所有编译／测试／格式检查在 GitHub Actions；真实官方 Obsidian 与 TaskNotes 的资产锁在 scripts/obsidian-fixture-lock.json。

Action 37573959830 的 compatibility 通过真实六事件 UI：创建、更新（包含改名）、完成、归档搬移、取消归档、删除均映射同一个 Paca 任务。其 build 因严格格式检查失败，未发布、未部署；应用 Action 格式补丁后必须重新通过完整检查才能发行。

实际发现并修复：dateModified 不是事件版本；官方缓存刷新 details 缺失与空值不同；归档搬移无 previous 且改变标签；未关联占位不能阻塞已验证来源，但真实创建不允许合并。升级核实旧快照哈希、保留来源标记和墓碑，归档使用明确的项目状态映射。

原 TaskNotes 不修改；额外缓存更新是官方行为，不按事件数量恰好为六断言。
