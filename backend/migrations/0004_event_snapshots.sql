-- Additive upgrade: retain existing source IDs, markers and task associations.
ALTER TABLE sources ADD COLUMN event_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN task_modified_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN snapshot JSONB;
ALTER TABLE sources ADD COLUMN last_event TEXT NOT NULL DEFAULT '';
ALTER TABLE sources ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE sources DROP CONSTRAINT sources_connection_id_vault_key_source_key_key;
ALTER TABLE sources ADD CONSTRAINT sources_generation_key UNIQUE(connection_id,vault_key,source_key,generation);
CREATE INDEX sources_live_connection ON sources(connection_id,state);
ALTER TABLE connections ADD COLUMN archive_tag TEXT NOT NULL DEFAULT 'archived';
CREATE TABLE receiver_stats (connection_id UUID PRIMARY KEY REFERENCES connections(id), signature_failures BIGINT NOT NULL DEFAULT 0,last_signature_failure TIMESTAMPTZ);
UPDATE plugin_metadata SET version=4 WHERE id=1;
