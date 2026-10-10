-- Keep identity and anti-replay markers while removing obsolete history details.
ALTER TABLE inbox ADD COLUMN history_cleared_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN history_cleared_at TIMESTAMPTZ;
ALTER TABLE sync_objects ADD COLUMN history_cleared_at TIMESTAMPTZ;
CREATE TABLE history_cleanups (
 id UUID PRIMARY KEY,connection_id UUID NOT NULL REFERENCES connections(id),
 cutoff TIMESTAMPTZ NOT NULL,deleted_tasks BIGINT NOT NULL,processed_events BIGINT NOT NULL,
 obsolete_sources BIGINT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
UPDATE plugin_metadata SET version=7 WHERE id=1;
