package worker

import (
	"context"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"github.com/jackc/pgx/v5"
	"net/url"
	"regexp"
	"strings"
)

func parentPath(ref string) string {
	if strings.HasPrefix(ref, "[[") && strings.HasSuffix(ref, "]]") {
		ref = strings.TrimSuffix(strings.TrimPrefix(ref, "[["), "]]")
		ref = strings.SplitN(ref, "|", 2)[0]
	}
	if match := regexp.MustCompile(`^\[[^\]]*\]\((.*?)\)$`).FindStringSubmatch(ref); len(match) == 2 {
		ref = strings.Trim(match[1], "<>")
		decoded, err := url.PathUnescape(ref)
		if err != nil {
			return ""
		}
		ref = decoded
	}
	if !strings.HasSuffix(ref, ".md") {
		ref += ".md"
	}
	ref = tasknotes.NormalizePath(ref)
	if !tasknotes.ValidPath(ref) {
		return ""
	}
	return ref
}

// A materialized TaskNotes period attaches to the server's existing date identity;
// neither title nor delayed webhook arrival is allowed to create a second period.
func (w *Worker) occurrenceSource(ctx context.Context, c connection, e *tasknotes.Envelope) (bool, error) {
	if e.Data.Task.Parent == "" || e.Data.Task.OccurrenceDate == "" {
		return false, nil
	}
	config, err := w.syncConfig(ctx, c.ID)
	if err != nil {
		return true, err
	}
	if config.Mode != "enabled" || !config.Recurrence {
		return false, nil
	}
	parentID, err := w.resolveSeries(ctx, config, e.Data.Task.Parent)
	if errors.Is(err, pgx.ErrNoRows) {
		var deleted bool
		_ = w.Pool.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM sync_objects WHERE id::text=$1 AND connection_id=$2 AND deleted)",e.Data.Task.Parent,c.ID).Scan(&deleted)
		if deleted { return true,associationConflict{"循环母任务已删除，本期事件已停止。"} }
		return true, associationPending{"正在等待循环母任务关联，已保留本期事件。"}
	}
	if err != nil {
		return true, err
	}
	if err = w.ensureRequestedPeriod(ctx, config, parentID, e.Data.Task.OccurrenceDate); err != nil {
		return true, err
	}
	var taskID, ref, objectID string
	err = w.Pool.QueryRow(ctx, "SELECT COALESCE(o.paca_task_id::text,''),o.source_ref,o.id::text FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.series_id=$1 AND p.occurrence_date=$2 AND p.state NOT IN('deleted','cancelled')", parentID, e.Data.Task.OccurrenceDate).Scan(&taskID, &ref, &objectID)
	if errors.Is(err, pgx.ErrNoRows) || taskID == "" {
		return true, associationPending{"正在等待服务器建立对应周期，已保留本期事件。"}
	}
	if err != nil {
		return true, err
	}
	if marker:=noteMarker(e.Data.Task); marker!="" && marker!=objectID && marker!=parentID { return true,associationConflict{"周期日期与笔记同步标记不一致，已停止关联。"} }
	e.Data.Task.Parent = parentID
	if e.Data.Previous != nil {
		e.Data.Previous.Parent = parentID
	}
	tx,err:=w.Pool.Begin(ctx)
	if err!=nil{return true,err}
	defer tx.Rollback(ctx)
	if _,err=linkSource(ctx,tx,c.ID,taskID,ref,e.Data.Task);err!=nil{return true,err}
	return true,tx.Commit(ctx)
}
func ruleFieldsOnly(updates tasksync.Snapshot) tasksync.Snapshot {
	out := tasksync.Snapshot{}
	for key, value := range updates {
		if tasksync.Fields[key] {
			out[key] = value
		}
	}
	return out
}
