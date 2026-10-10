ALTER TABLE sources ADD COLUMN verified_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN verification_error TEXT NOT NULL DEFAULT '';
CREATE INDEX inbox_cleared_hash ON inbox(connection_id,body_hash) WHERE history_cleared_at IS NOT NULL;
UPDATE plugin_metadata SET version=8 WHERE id=1;
