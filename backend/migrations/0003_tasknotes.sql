CREATE TABLE connections (
 id UUID PRIMARY KEY, project_id UUID NOT NULL, name TEXT NOT NULL,
 secret_enc TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE,
 timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai', status_map JSONB NOT NULL DEFAULT '{}',
 priority_map JSONB NOT NULL DEFAULT '{"low":10,"normal":35,"high":75}',
 revision INTEGER NOT NULL DEFAULT 1, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX connections_project ON connections(project_id);
CREATE TABLE inbox (
 id BIGSERIAL PRIMARY KEY, connection_id UUID NOT NULL REFERENCES connections(id),
 delivery_id TEXT NOT NULL, body JSONB NOT NULL, body_hash TEXT NOT NULL, event TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt TIMESTAMPTZ NOT NULL DEFAULT NOW(), received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 lease_owner TEXT, lease_until TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(connection_id,delivery_id)
);
CREATE INDEX inbox_pending ON inbox(state,next_attempt,id);
CREATE TABLE sources (
 id BIGSERIAL PRIMARY KEY, connection_id UUID NOT NULL REFERENCES connections(id),
 vault_key TEXT NOT NULL, source_key TEXT NOT NULL, paca_task_id UUID,
 external_ref TEXT NOT NULL UNIQUE, state TEXT NOT NULL DEFAULT 'new',
 version_at TIMESTAMPTZ, snapshot_hash TEXT NOT NULL DEFAULT '', source_tags JSONB NOT NULL DEFAULT '[]',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(connection_id,vault_key,source_key)
);
CREATE TABLE path_aliases (
 connection_id UUID NOT NULL, vault_key TEXT NOT NULL, path TEXT NOT NULL,
 source_id BIGINT NOT NULL REFERENCES sources(id), PRIMARY KEY(connection_id,vault_key,path)
);
CREATE TABLE audit_log (
 id BIGSERIAL PRIMARY KEY, project_id UUID NOT NULL, actor_id TEXT NOT NULL,
 action TEXT NOT NULL, subject TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
UPDATE plugin_metadata SET version=3 WHERE id=1;
