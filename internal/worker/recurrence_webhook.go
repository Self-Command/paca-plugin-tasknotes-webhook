package worker

import (
	"context"
	"encoding/json"
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
		return true, errors.New("正在等待循环母任务关联，已保留本期事件。")
	}
	if err != nil {
		return true, err
	}
	if err = w.ensureRequestedPeriod(ctx, config, parentID, e.Data.Task.OccurrenceDate); err != nil {
		return true, err
	}
	var taskID, ref string
	err = w.Pool.QueryRow(ctx, "SELECT COALESCE(o.paca_task_id::text,''),o.source_ref FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.series_id=$1 AND p.occurrence_date=$2 AND p.state NOT IN('deleted','cancelled')", parentID, e.Data.Task.OccurrenceDate).Scan(&taskID, &ref)
	if errors.Is(err, pgx.ErrNoRows) || taskID == "" {
		return true, errors.New("正在等待服务器建立对应周期，已保留本期事件。")
	}
	if err != nil {
		return true, err
	}
	e.Data.Task.Parent = parentID
	if e.Data.Previous != nil {
		e.Data.Previous.Parent = parentID
	}
	raw, _ := json.Marshal(e.Data.Task)
	var sourceID int64
	err = w.Pool.QueryRow(ctx, "INSERT INTO sources(connection_id,vault_key,source_key,paca_task_id,external_ref,state,snapshot) VALUES($1,$2,$3,$4,$5,'linked',$6::jsonb) ON CONFLICT(external_ref) DO UPDATE SET paca_task_id=EXCLUDED.paca_task_id RETURNING id", c.ID, e.Vault.Path, e.Data.Task.Path, taskID, ref, string(raw)).Scan(&sourceID)
	if err != nil {
		return true, err
	}
	_, err = w.Pool.Exec(ctx, "INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,$2,$3,$4) ON CONFLICT(connection_id,vault_key,path) DO NOTHING", c.ID, e.Vault.Path, e.Data.Task.Path, sourceID)
	return true, err
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
