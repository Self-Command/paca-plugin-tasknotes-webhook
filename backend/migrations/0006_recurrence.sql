-- Additive, inactive until the selected connection enables recurrence.
ALTER TABLE connections ADD COLUMN recurrence_enabled BOOLEAN NOT NULL DEFAULT FALSE;
CREATE UNIQUE INDEX recurrence_single_coordinator ON connections(project_id) WHERE enabled AND sync_mode='enabled' AND recurrence_enabled;
CREATE TABLE recurring_series (
 object_id UUID PRIMARY KEY REFERENCES sync_objects(id),
 connection_id UUID NOT NULL REFERENCES connections(id),
 rule_revision BIGINT NOT NULL DEFAULT 1,
 rule_hash TEXT NOT NULL,
 definition JSONB NOT NULL,
 state TEXT NOT NULL DEFAULT 'active' CHECK(state IN('active','paused','cancelled','conflict')),
 last_reconcile TIMESTAMPTZ,
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE recurring_periods (
 series_id UUID NOT NULL REFERENCES recurring_series(object_id),
 occurrence_date DATE NOT NULL,
 object_id UUID NOT NULL UNIQUE REFERENCES sync_objects(id),
 rule_revision BIGINT NOT NULL,
 state TEXT NOT NULL DEFAULT 'planned' CHECK(state IN('planned','completed','skipped','deleted','cancelled','conflict')),
 expected JSONB NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(series_id,occurrence_date)
);
CREATE INDEX recurring_periods_state ON recurring_periods(series_id,state);
UPDATE plugin_metadata SET version=6 WHERE id=1;
