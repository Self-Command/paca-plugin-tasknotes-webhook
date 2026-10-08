-- Additive upgrade; existing source markers and accepted task snapshots remain intact.
ALTER TABLE connections ADD COLUMN sync_mode TEXT NOT NULL DEFAULT 'off' CHECK(sync_mode IN ('off','preview','enabled'));
ALTER TABLE connections ADD COLUMN reverse_status_map JSONB NOT NULL DEFAULT '{}';
ALTER TABLE connections ADD COLUMN reverse_priority_map JSONB NOT NULL DEFAULT '{}';
ALTER TABLE connections ADD COLUMN vault_id TEXT;
ALTER TABLE connections ADD COLUMN task_tag TEXT NOT NULL DEFAULT 'task';
CREATE TABLE sync_objects (
 id UUID PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),paca_task_id UUID,
 source_id BIGINT REFERENCES sources(id),kind TEXT NOT NULL DEFAULT 'task' CHECK(kind IN('task','series','occurrence')),
 revision BIGINT NOT NULL DEFAULT 1,snapshot JSONB NOT NULL DEFAULT '{}',paca_snapshot JSONB NOT NULL DEFAULT '{}',
 deleted BOOLEAN NOT NULL DEFAULT FALSE,path TEXT NOT NULL DEFAULT '',note_created TEXT NOT NULL DEFAULT '',
 binding_state TEXT NOT NULL DEFAULT 'unbound',binding_owner TEXT,binding_until TIMESTAMPTZ,
 source_ref TEXT NOT NULL,path_aliases JSONB NOT NULL DEFAULT '[]',last_error TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),UNIQUE(connection_id,paca_task_id),UNIQUE(connection_id,source_ref)
);
CREATE TABLE sync_changes(cursor BIGSERIAL PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),object_id UUID NOT NULL REFERENCES sync_objects(id),revision BIGINT NOT NULL,payload JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE INDEX sync_changes_scope ON sync_changes(connection_id,cursor);
CREATE TABLE sync_credentials(id UUID PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),token_hash TEXT NOT NULL UNIQUE,enabled BOOLEAN NOT NULL DEFAULT TRUE,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE sync_devices(connection_id UUID NOT NULL REFERENCES connections(id),device_id TEXT NOT NULL,cursor BIGINT NOT NULL DEFAULT 0,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(connection_id,device_id));
CREATE TABLE sync_operations(id BIGSERIAL PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),object_id UUID REFERENCES sync_objects(id),device_id TEXT NOT NULL,op_id TEXT NOT NULL,body_hash TEXT NOT NULL,body JSONB NOT NULL,state TEXT NOT NULL DEFAULT 'pending',result JSONB NOT NULL DEFAULT '{}',attempts INTEGER NOT NULL DEFAULT 0,next_attempt TIMESTAMPTZ NOT NULL DEFAULT NOW(),lease_until TIMESTAMPTZ,error TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),UNIQUE(connection_id,op_id));
CREATE TABLE sync_receipts(id UUID PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),object_id UUID NOT NULL REFERENCES sync_objects(id),op_id TEXT NOT NULL,revision BIGINT NOT NULL,expected JSONB NOT NULL,state TEXT NOT NULL DEFAULT 'prepared',created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),UNIQUE(connection_id,op_id));
CREATE TABLE sync_conflicts(id UUID PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),object_id UUID NOT NULL REFERENCES sync_objects(id),operation_id BIGINT REFERENCES sync_operations(id),base_revision BIGINT NOT NULL,base JSONB NOT NULL,local JSONB NOT NULL,remote JSONB NOT NULL,fields JSONB NOT NULL,state TEXT NOT NULL DEFAULT 'open',created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE sync_scan_state(connection_id UUID PRIMARY KEY REFERENCES connections(id),last_complete TIMESTAMPTZ,last_error TEXT NOT NULL DEFAULT '');
CREATE TABLE sync_dirty(project_id UUID PRIMARY KEY,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
ALTER TABLE sync_operations ADD COLUMN inbox_id BIGINT REFERENCES inbox(id);
ALTER TABLE sync_receipts ADD COLUMN fields JSONB NOT NULL DEFAULT '[]';
ALTER TABLE sync_receipts ADD COLUMN actual JSONB;
ALTER TABLE sync_objects ADD COLUMN canonical_id UUID REFERENCES sync_objects(id);
UPDATE plugin_metadata SET version=5 WHERE id=1;
