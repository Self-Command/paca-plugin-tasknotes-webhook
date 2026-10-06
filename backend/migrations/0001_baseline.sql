CREATE TABLE IF NOT EXISTS plugin_metadata (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL);
INSERT INTO plugin_metadata(id, version) VALUES (1, 1) ON CONFLICT (id) DO NOTHING;
