package worker

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// Legacy sources may predate sync_objects. Only an explicit project-scoped 404
// proves the target is gone; network/authentication errors preserve the link.
func (w *Worker) verifyLegacySource(ctx context.Context) error {
	var id int64
	var connectionID, project, taskID string
	err := w.Pool.QueryRow(ctx, `SELECT s.id,c.id::text,c.project_id::text,s.paca_task_id::text
 FROM sources s JOIN connections c ON c.id=s.connection_id WHERE c.enabled AND s.state='linked'
 AND s.history_cleared_at IS NULL AND s.paca_task_id IS NOT NULL
 AND (s.verified_at IS NULL OR s.verified_at<NOW()-INTERVAL '1 minute')
 AND NOT EXISTS(SELECT 1 FROM sync_objects o WHERE o.connection_id=s.connection_id AND (o.source_id=s.id OR o.paca_task_id=s.paca_task_id))
 ORDER BY s.verified_at NULLS FIRST,s.id LIMIT 1`).Scan(&id,&connectionID,&project,&taskID)
	if errors.Is(err,pgx.ErrNoRows) { return nil }
	if err!=nil { return err }
	conn, err := w.Pool.Acquire(ctx)
	if err!=nil { return err }
	defer conn.Release()
	var locked bool
	if err=conn.QueryRow(ctx,"SELECT pg_try_advisory_lock(hashtextextended($1,0))",connectionID).Scan(&locked); err!=nil || !locked { return err }
	defer conn.Exec(context.Background(),"SELECT pg_advisory_unlock(hashtextextended($1,0))",connectionID)
	var current nativeTask
	err=w.call(ctx,"GET","/projects/"+project+"/tasks/"+taskID,nil,&current)
	state,message := "linked",""
	var api apiError
	if errors.As(err,&api) && api.Code==404 { state="deleted" } else if err!=nil || current.ID!=taskID { message="暂时无法核实对应任务，原关联保留。" }
	_, updateErr := conn.Exec(ctx,`UPDATE sources SET state=$3,verified_at=NOW(),verification_error=$4,
 updated_at=CASE WHEN $3='deleted' THEN NOW() ELSE updated_at END
 WHERE id=$1 AND paca_task_id=$2 AND state='linked' AND history_cleared_at IS NULL`,id,taskID,state,message)
	return updateErr
}
