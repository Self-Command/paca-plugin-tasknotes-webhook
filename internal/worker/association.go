package worker

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Only a never-created placeholder may be merged; real/uncertain creations never are.
func adoptPlaceholder(ctx context.Context, tx pgx.Tx, placeholder, linked int64) (bool, error) {
	var safe bool
	err := tx.QueryRow(ctx, "SELECT a.state='unassociated' AND a.paca_task_id IS NULL AND b.state='linked' AND b.paca_task_id IS NOT NULL AND a.connection_id=b.connection_id FROM sources a CROSS JOIN sources b WHERE a.id=$1 AND b.id=$2", placeholder, linked).Scan(&safe)
	if err != nil || !safe {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE path_aliases SET source_id=$1 WHERE source_id=$2", linked, placeholder); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE sources SET state='superseded',updated_at=NOW() WHERE id=$1", placeholder); err != nil {
		return false, err
	}
	return true, nil
}
