# TaskNotes 接入

Independent Apache-2.0 plugin for official Paca v0.18.6.

Status: signed ingestion, durable inbox, REST task synchronization and project settings are implemented;
business acceptance is tracked in Actions. Phase-0 releases are scaffolds and must not be used as a working integration.
All builds and automated checks run in GitHub Actions. No Paca core fork is required by the baseline.

Each release contains its own WASM, frontend, migrations and independent worker image. Never mix versions.
The TaskNotes integration is desktop-only and one-way; the PushGo plugin can run without TaskNotes.
Production credentials are runtime configuration only.

## Features

- Official TaskNotes 4.13.8 desktop webhook protocol, HMAC-SHA256 over exact raw bytes.
- Project connections with encrypted secrets, status / importance mapping and optimistic settings revision.
- Durable, deduplicated inbox; a reused Delivery-ID with different bytes is rejected.
- Stable Paca task associations, previous-path aliases, source timestamps and deletion tombstones.
- REST-only core writes; preserve unrelated Paca fields and unrelated tags.
- Persist creation intent before POST. A lost response triggers marker reconciliation, never a blind second create.
- Explicit day / instant precision metadata, including timezone and DST ambiguity checks.
- Separate native worker, PostgreSQL session locks and persisted leases. Host disable or version mismatch stops external operations.
- Project settings: connection configuration, recent deliveries, reprocessing and manual association.

## Install and configure

Use a verified release's `plugin-install.tar.gz`. Extract its `wasm/<plugin-id>` into the API's
`PLUGINS_WASM_DIR`, and `frontend/<plugin-id>` into the gateway's plugin asset directory.
Install the included manifest through Paca's plugin administration. Preserve the migration files.
Paca needs a valid 64-hex-character `ENCRYPTION_KEY`; missing encryption fails closed.

1. Create a restricted Paca integration account with task read/create/update/delete and task-status read permission only in the intended project.
2. Create that account's personal API key. Store it in a restricted runtime secret file.
3. As an administrator with `tasknotes_webhook.worker_manage`, POST `{}` to
   `/api/v1/plugins/com.selfcommand.tasknotes-webhook/admin/worker-credential`.
   Save the returned secret in the worker secret file; it is shown once.
4. Give the worker a PostgreSQL login with access only to
   `plugin_data_com_selfcommand_tasknotes_webhook`, including its sequences. It does not need access to core tables.
5. Start the exact release's worker image with the runtime environment below.
6. In the project's **TaskNotes 接入** tab create a connection, then copy its receiver URL into official TaskNotes' desktop Webhook settings.
   TaskNotes' settings UI **generates its own Secret**. Copy that value back into Paca's **TaskNotes 生成的 Secret** field and save the connection.
   Leaving the field blank on update preserves the saved encrypted value. Queries never return it. Saving a Secret preserves the connection, task associations and delivery history.
   Enable custom webhook headers (`corsHeaders`) and subscribe to the six task events displayed in the settings page.
   For API-based TaskNotes registration only, Paca can generate a Secret for the optional `secret` request property; this is not an input field in TaskNotes' settings UI.
7. Leave webhook transformations off: the receiver expects the official envelope. Unknown/custom statuses require an explicit Paca status UUID mapping.

Runtime environment (no build secrets):

```dotenv
PACA_API_URL=http://api:8080
DATABASE_URL=postgres://tasknotes_worker:<runtime-password>@postgres:5432/paca?sslmode=disable
PACA_API_KEY_FILE=/run/secrets/paca_api_key
WORKER_SECRET_FILE=/run/secrets/worker_secret
```

Only the signed `/receive/<connection-id>` endpoint needs public access. Block `/worker/control`
at the public reverse proxy; workers call it on the private Docker network.
`/admin/worker-credential` requires administrator permission. Query APIs never return saved secrets.
Do not share an administrator API key with a production worker.

## Recovery and limits

`pending` / `error` deliveries retry with backoff. `uncertain` deliveries query the external marker;
zero matching tasks is not proof that the original create failed. Confirm the result and use manual association
before reprocessing. Multiple matches and same-timestamp changes require a human decision.
An update at a previously unseen path is held for manual association: it may be an undocumented rename
or a missed creation. Resolve same-timestamp differences in TaskNotes and save a new version before
reprocessing; the receiver never guesses a winner or merges tasks by title.
Applied deliveries cannot be replayed. Reprocessing retains creation intent and tombstones.

TaskNotes is the owner of title, dates, status, importance and source tags. Paca v0.18.6 stores its native dates as SQL DATE, so accurate source instants remain in `_integration_state_v1` version 2. The native field holds the calendar day in the configured timezone. The `start_core_date` / `due_core_date` metadata binds each accurate instant to that native day; changing the native day invalidates precision. Date-only source values are retained in metadata with null native dates, so no midnight reminder is inferred.
Paca edits to these fields can
be replaced by the next accepted TaskNotes event. No changes are sent back to Obsidian.
Paca v0.18.6 has no task archive field: TaskNotes archive is recorded in `_integration_state_v1.archived`
so a compatible scheduler can stop reminders. It does not delete the Paca task.
Recurring parents are imported without direct reminders. Enable the project's single recurrence coordinator to materialize individual periods using the pinned official TaskNotes model. Each period has a stable series/date identity and its own reminder and check-in history.
The official desktop sender does not maintain a durable offline journal; this plugin cannot recover events it never received.

## Verification

Every build runs Go race tests/vet, strict formatting, TinyGo WASM, frontend type checking,
official Paca installation/disable/restart tests and browser settings loading. Actions load the pinned official
TaskNotes WebhookController with a mocked Obsidian transport to produce real payloads, then verify
create/update/delete, source precision, unrelated tags, stale events and a simulated lost successful create response.
See the `host-verification` artifact for actual results; a source commit alone is not evidence of passing checks.


## v2.0 六事件修复

使用事件时间与独立任务修改时间、可验证的前序快照和来源墓碑；归档映射使用 `status_map["@archived"]` 的项目状态 UUID。旧 schema 3 关联原地升级至 schema 4，不重建任务。

所有验证在 Action：控制器协议测试及真实官方 Obsidian 1.14.4 / TaskNotes 4.13.8 隔离 vault 界面测试。`scripts/obsidian-fixture-lock.json` 锁发行资产 SHA256；实际发行源码为 `1ba391b957c6fbbdc484716bbc5a284f5f02e59d`，审查提交 `69535cd956d11474b980deef8429ff0be232185f` 较它多四个提交，二者分别记录，不以自定义插件产物代替。

数据库访问为每个插件加独立查询标识，避免官方宿主共享 PostgreSQL 连接池在切换 schema 后复用其他插件的缓存执行计划。保持独立 schema、角色和 API 边界，使用三个插件的匹配发行产物。


## WASM 凭据与重载

WASM 后端的随机编号、配对令牌、worker 凭据及 AES-GCM nonce 使用原生 PostgreSQL 的随机 UUID 组合获取新鲜随机数据，避免模块状态恢复后复用历史序列。无需额外数据库扩展；原密文格式保持兼容。原生 Go worker 保留操作系统随机源。Action 包含错误时拒绝生成凭据的检查，打卡插件另验收连续配对、撤销后重新配对、模块重载与宿主重启。
