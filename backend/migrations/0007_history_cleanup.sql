-- Keep identity and anti-replay markers while removing obsolete history details.
ALTER TABLE inbox ADD COLUMN history_cleared_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN history_cleared_at TIMESTAMPTZ;
ALTER TABLE sync_objects ADD COLUMN history_cleared_at TIMESTAMPTZ;
UPDATE plugin_metadata SET version=7 WHERE id=1;
