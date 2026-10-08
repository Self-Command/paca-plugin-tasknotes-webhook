package worker

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/jackc/pgx/v5"
)

type associationPending struct{ message string }

func (e associationPending) Error() string { return e.message }

func associationWaitExhausted(attempts int, received, now time.Time) bool {
	return attempts >= 9 || !now.Before(received.Add(30*time.Minute))
}

func noteMarker(t tasknotes.Task) string {
	if t.Details == nil {
		return ""
	}
	m := regexp.MustCompile(`<!-- paca-sync-id:([a-fA-F0-9-]{36}) -->`).FindStringSubmatch(*t.Details)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// The external identity wins over paths. A live path is never silently rebound
// to another period, including when an official API accidentally returns it.
func linkSource(ctx context.Context, tx pgx.Tx, connection, task, ref string, incoming tasknotes.Task) (int64, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "source-identity:"+connection); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(ctx, "SELECT id FROM sources WHERE external_ref=$1 AND connection_id=$2 FOR UPDATE", ref, connection).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	old, oldErr := sourceForPath(ctx, tx, connection, incoming.Path)
	if oldErr != nil && !errors.Is(oldErr, pgx.ErrNoRows) {
		return 0, oldErr
	}
	if oldErr == nil && old != id {
		var state string
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT state,snapshot FROM sources WHERE id=$1 FOR UPDATE", old).Scan(&state, &raw); err != nil {
			return 0, err
		}
		var previous tasknotes.Task
		_ = json.Unmarshal(raw, &previous)
		if state != "deleted" && state != "superseded" || incoming.DateCreated == "" || previous.DateCreated == "" || incoming.DateCreated == previous.DateCreated {
			return 0, associationConflict{"笔记路径已关联另一任务或周期，请核对母任务、周期日期和同步标记。"}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM path_aliases WHERE connection_id=$1 AND path=$2", connection, incoming.Path); err != nil {
			return 0, err
		}
	}
	if id == 0 {
		raw, _ := json.Marshal(incoming)
		err = tx.QueryRow(ctx, "INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot,generation) SELECT $1,'connection',$2,$3,$4,'linked',$5::jsonb,COALESCE(MAX(generation),0)+1 FROM sources WHERE connection_id=$1 AND vault_key='connection' AND source_key=$2 RETURNING id", connection, incoming.Path, task, ref, string(raw)).Scan(&id)
		if err != nil {
			return 0, err
		}
	} else {
		var existing, state string
		if err = tx.QueryRow(ctx, "SELECT COALESCE(paca_task_id::text,''),state FROM sources WHERE id=$1", id).Scan(&existing, &state); err != nil {
			return 0, err
		}
		if state == "conflict" {
			return 0, associationConflict{"该来源关联已隔离，请核对周期身份后再处理。"}
		}
		if existing != "" && existing != task {
			return 0, associationConflict{"稳定来源标记对应不同任务，请核对关联。"}
		}
		if _, err = tx.Exec(ctx, "UPDATE sources SET paca_task_id=$2 WHERE id=$1", id, task); err != nil {
			return 0, err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,'connection',$2,$3) ON CONFLICT(connection_id,vault_key,path) DO UPDATE SET source_id=EXCLUDED.source_id", connection, incoming.Path, id)
	return id, err
}
