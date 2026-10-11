-- Core events retain server ordering independently of client clocks and worker delays.
CREATE TABLE sync_native_events (
 id UUID PRIMARY KEY,project_id UUID NOT NULL,task_id UUID NOT NULL,
 topic TEXT NOT NULL,occurred_at TIMESTAMPTZ NOT NULL,content JSONB NOT NULL
);
CREATE INDEX sync_native_events_task_order ON sync_native_events(task_id,occurred_at);
UPDATE plugin_metadata SET version=9 WHERE id=1;
