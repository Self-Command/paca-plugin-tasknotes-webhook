package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"github.com/jackc/pgx/v5"
	"time"
)

// Resolve only stable IDs and recorded paths, never titles.
func (w *Worker) resolveSeries(ctx context.Context, c syncConfig, reference string) (string, error) {
	var id string
	if syncUUID.MatchString(reference) {
		err := w.Pool.QueryRow(ctx, "SELECT id::text FROM sync_objects WHERE id=$1 AND connection_id=$2 AND kind='series' AND NOT deleted", reference, c.ID).Scan(&id)
		return id, err
	}
	path := parentPath(reference)
	if path == "" {
		return "", errors.New("循环母任务路径无效，请核对关联。")
	}
	err := w.Pool.QueryRow(ctx, "SELECT o.id::text FROM sync_objects o WHERE o.connection_id=$1 AND o.kind='series' AND NOT o.deleted AND (o.path=$2 OR o.path_aliases ? $2 OR EXISTS(SELECT 1 FROM sources s JOIN path_aliases a ON a.source_id=s.id WHERE s.paca_task_id=o.paca_task_id AND s.connection_id=$1 AND a.path=$2))", c.ID, path).Scan(&id)
	return id, err
}

// Upgrades keep the existing Paca task, source, binding and check-in history.
// Ambiguous dates block reconciliation instead of silently merging two tasks.
func (w *Worker) adoptKnownPeriods(ctx context.Context, c syncConfig, series string, ruleRevision int64) error {
	rows, err := w.Pool.Query(ctx, "SELECT o.id::text,o.snapshot,o.revision,o.deleted FROM sync_objects o WHERE o.connection_id=$1 AND o.kind='occurrence' AND o.canonical_id IS NULL AND NOT EXISTS(SELECT 1 FROM recurring_periods p WHERE p.object_id=o.id) ORDER BY o.id", c.ID)
	if err != nil {
		return err
	}
	type candidate struct {
		id, date string
		snapshot tasksync.Snapshot
		revision int64
		deleted  bool
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		var raw []byte
		if err = rows.Scan(&item.id, &raw, &item.revision, &item.deleted); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(raw, &item.snapshot) == nil {
			items = append(items, item)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	selected := map[string]candidate{}
	for _, item := range items {
		reference, _ := item.snapshot["recurrence_parent"].(string)
		if reference == "" {
			continue
		}
		parent, resolveErr := w.resolveSeries(ctx, c, reference)
		if errors.Is(resolveErr, pgx.ErrNoRows) {
			continue
		}
		if resolveErr != nil {
			return resolveErr
		}
		if parent != series {
			continue
		}
		item.date, _ = item.snapshot["occurrence_date"].(string)
		if parsed, parseErr := time.Parse("2006-01-02", item.date); parseErr != nil || parsed.Format("2006-01-02") != item.date {
			return errors.New("已有周期的日期无效，请核对关联。")
		}
		if _, exists := selected[item.date]; exists {
			return errors.New("同一周期日期存在多个任务，请先核对关联。")
		}
		selected[item.date] = item
	}
	for _, item := range selected {
		var existing string
		lookup := w.Pool.QueryRow(ctx, "SELECT object_id::text FROM recurring_periods WHERE series_id=$1 AND occurrence_date=$2", series, item.date).Scan(&existing)
		if lookup == nil {
			if existing != item.id {
				return errors.New("本期已有其他关联任务，请先核对，未创建新任务。")
			}
			continue
		}
		if !errors.Is(lookup, pgx.ErrNoRows) {
			return lookup
		}
		raw, _ := json.Marshal(item.snapshot)
		state := "planned"
		if item.deleted {
			state = "deleted"
		}
		tx, beginErr := w.Pool.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		inserted, insertErr := tx.Exec(ctx, "INSERT INTO recurring_periods(series_id,occurrence_date,object_id,rule_revision,expected,state) VALUES($1,$2,$3,$4,$5::jsonb,$6) ON CONFLICT(series_id,occurrence_date) DO NOTHING", series, item.date, item.id, ruleRevision, string(raw), state)
		if insertErr != nil {
			tx.Rollback(ctx)
			return insertErr
		}
		if inserted.RowsAffected() != 1 {
			tx.Rollback(ctx)
			return errors.New("周期关联已变化，请刷新后核对。")
		}
		if !item.deleted && item.snapshot["recurrence_parent"] != series {
			op := tasksync.Operation{OpID: "series:adopt:" + item.id, SyncID: item.id, BaseRevision: item.revision, Kind: "update", Base: item.snapshot, Changes: tasksync.Snapshot{"recurrence_parent": series}}
			body, _ := json.Marshal(op)
			if _, err = tx.Exec(ctx, "INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,'recurrence',$3,$4,$5::jsonb) ON CONFLICT(connection_id,op_id) DO NOTHING", c.ID, item.id, op.OpID, tasksync.Hash(op), string(body)); err != nil {
				tx.Rollback(ctx)
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Explicit official materialization can refer to a past or more distant date.
// Only that requested date is registered; it does not backfill past reminders.
func (w *Worker) ensureRequestedPeriod(ctx context.Context, c syncConfig, series, date string) error {
	var raw []byte
	var revision int64
	if err := w.Pool.QueryRow(ctx, "SELECT definition,rule_revision FROM recurring_series WHERE object_id=$1 AND connection_id=$2 AND state='active'", series, c.ID).Scan(&raw, &revision); err != nil {
		return err
	}
	var definition tasksync.Snapshot
	if err := json.Unmarshal(raw, &definition); err != nil {
		return err
	}
	if err := w.adoptKnownPeriods(ctx, c, series, revision); err != nil {
		return err
	}
	var state string
	err := w.Pool.QueryRow(ctx, "SELECT state FROM recurring_periods WHERE series_id=$1 AND occurrence_date=$2", series, date).Scan(&state)
	if err == nil {
		if state == "deleted" || state == "cancelled" {
			return associationConflict{"本期已删除或取消，请核对后重新安排。"}
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return err
	}
	now := time.Now().In(loc)
	output, err := recurrenceModel(ctx, c.Timezone, map[string]any{"mode": "materialize", "task": definition, "date": date, "today": now.Format("2006-01-02"), "now": now.Format(time.RFC3339), "series_id": series})
	if err != nil {
		return err
	}
	if len(output.Periods) != 1 || output.Periods[0].Date != date {
		return errors.New("本期日期无法确认。")
	}
	return w.insertPeriod(ctx, c, series, date, revision, normalizedPeriod(definition, output.Periods[0], series))
}

func (w *Worker) insertPeriod(ctx context.Context, c syncConfig, series, date string, revision int64, period tasksync.Snapshot) error {
	periodID := tasksync.ID(series, date)
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO sync_objects(id,connection_id,source_ref,kind) VALUES($1,$2,$3,'occurrence') ON CONFLICT(id) DO NOTHING", periodID, c.ID, "period:"+series+":"+date); err != nil {
		return err
	}
	raw, _ := json.Marshal(period)
	inserted, err := tx.Exec(ctx, "INSERT INTO recurring_periods(series_id,occurrence_date,object_id,rule_revision,expected) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(series_id,occurrence_date) DO NOTHING", series, date, periodID, revision, string(raw))
	if err != nil {
		return err
	}
	if inserted.RowsAffected() == 1 {
		op := tasksync.Operation{OpID: "period-create:" + periodID, SyncID: periodID, Kind: "create", Base: tasksync.Snapshot{}, Changes: period}
		body, _ := json.Marshal(op)
		_, err = tx.Exec(ctx, "INSERT INTO sync_operations(connection_id,object_id,device_id,op_id,body_hash,body) VALUES($1,$2,'recurrence',$3,$4,$5::jsonb) ON CONFLICT(connection_id,op_id) DO NOTHING", c.ID, periodID, op.OpID, tasksync.Hash(op), string(body))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
