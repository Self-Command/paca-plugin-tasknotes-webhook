package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"net/http"
	"strings"
	"time"
)

func (w *Worker) periodFrozen(ctx context.Context, c syncConfig, task string) (bool, error) {
	if w.CheckinURL == "" || task == "" {
		return false, nil
	}
	raw, _ := json.Marshal(map[string]string{"project_id": c.Project, "task_id": task})
	req, err := http.NewRequestWithContext(ctx, "POST", w.CheckinURL+"/internal/v1/times/freeze", bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.CheckinSecret)
	response, err := w.HTTP.Do(req)
	if err != nil {
		return false, errors.New("打卡窗口暂时无法核对，已暂停修改本期时间。")
	}
	defer response.Body.Close()
	var result struct {
		Enabled bool  `json:"enabled"`
		Frozen  *bool `json:"frozen"`
	}
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&result) != nil {
		return false, errors.New("打卡窗口暂时无法核对，已暂停修改本期时间。")
	}
	if !result.Enabled {
		return false, nil
	}
	if result.Frozen == nil {
		return false, errors.New("请先升级打卡插件以核对冻结窗口。")
	}
	return *result.Frozen, nil
}
var errScheduleFrozen = errors.New("打卡窗口已开放，时间已冻结。请先取消当前打卡安排再修改。")

func (w *Worker) guardScheduleChange(ctx context.Context, c syncConfig, task string, before, next tasksync.Snapshot) error {
 changed := false
 for _, field := range []string{"scheduled", "due", "recurrence", "recurrence_anchor"} {
  if !tasksync.Equal(before[field], next[field]) { changed = true; break }
 }
 if !changed || task == "" { return nil }
 frozen, err := w.periodFrozen(ctx, c, task)
 if err != nil { return err }
 if frozen { return errScheduleFrozen }
 return nil
}

func includesDate(value any, date string) bool {
	raw, _ := json.Marshal(value)
	var items []string
	_ = json.Unmarshal(raw, &items)
	for _, v := range items {
		if v == date {
			return true
		}
	}
	return false
}
func (w *Worker) advanceCompletedPeriods(ctx context.Context, c syncConfig, series string, parent tasksync.Snapshot) (bool, error) {
	var statuses struct {
		Items []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err := w.call(ctx, "GET", "/projects/"+c.Project+"/task-statuses", nil, &statuses); err != nil {
		return false, err
	}
	completed := map[string]bool{}
	done := ""
	for _, s := range statuses.Items {
		if s.Category == "done" && s.ID != c.Statuses["@archived"] {
			completed[s.ID] = true
			if done == "" {
				done = c.Reverse[s.ID]
				for name, statusID := range c.Statuses {
					if done == "" && statusID == s.ID && !strings.HasPrefix(name, "@") {
						done = name
					}
				}
			}
		}
	}
	rows, err := w.Pool.Query(ctx, "SELECT p.object_id::text,p.occurrence_date::text,o.snapshot,o.paca_snapshot,o.revision,p.state FROM recurring_periods p JOIN sync_objects o ON o.id=p.object_id WHERE p.series_id=$1 AND p.state IN('planned','completed','skipped') AND NOT o.deleted ORDER BY p.occurrence_date", series)
	if err != nil {
		return false, err
	}
	type item struct {
		id, date, state string
		revision        int64
		current         tasksync.Snapshot
		native          nativeTask
	}
	items := []item{}
	for rows.Next() {
		var p item
		var current, native []byte
		if err = rows.Scan(&p.id, &p.date, &current, &native, &p.revision, &p.state); err != nil {
			rows.Close()
			return false, err
		}
		_ = json.Unmarshal(current, &p.current)
		_ = json.Unmarshal(native, &p.native)
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, p := range items {
		var pending bool
		if err = w.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sync_operations WHERE object_id=$1 AND state IN('pending','retry','sending','conflict','uncertain'))", p.id).Scan(&pending); err != nil {
			return false, err
		}
		if pending {
			continue
		}
		if p.state == "completed" && !completed[p.native.Status] && p.current["archived"] != true {
			if includesDate(parent["complete_instances"], p.date) {
				loc, _ := time.LoadLocation(c.Timezone)
				now := time.Now().In(loc)
				result, modelErr := recurrenceModel(ctx, c.Timezone, map[string]any{"mode": "progress", "undo": true, "task": parent, "today": now.Format("2006-01-02"), "now": now.Format(time.RFC3339), "date": p.date, "active_status": p.current["status"]})
				if modelErr != nil {
					return false, modelErr
				}
				var revision int64
				if err = w.Pool.QueryRow(ctx, "SELECT revision FROM sync_objects WHERE id=$1", series).Scan(&revision); err != nil {
					return false, err
				}
				if err = w.queuePeriodOperation(ctx, c, series, revision, "update", parent, ruleFieldsOnly(result.Updates), fmt.Sprintf("undo:%s:%d", p.id, p.revision)); err != nil {
					return false, err
				}
				return true, nil
			}
			if _, err = w.Pool.Exec(ctx, "UPDATE recurring_periods SET state='planned',updated_at=NOW() WHERE object_id=$1", p.id); err != nil {
				return false, err
			}
			continue
		}
		if p.state == "skipped" && !includesDate(parent["skipped_instances"], p.date) {
			if err = w.queuePeriodOperation(ctx, c, p.id, p.revision, "update", p.current, tasksync.Snapshot{"archived": false}, fmt.Sprintf("unskip:%s:%d", p.id, p.revision)); err != nil {
				return false, err
			}
			if _, err = w.Pool.Exec(ctx, "UPDATE recurring_periods SET state='planned',updated_at=NOW() WHERE object_id=$1", p.id); err != nil {
				return false, err
			}
			continue
		}
		skipped := includesDate(parent["skipped_instances"], p.date)
		if includesDate(parent["complete_instances"], p.date) || skipped {
			state := "completed"
			if skipped {
				state = "skipped"
			}
			if _, err = w.Pool.Exec(ctx, "UPDATE recurring_periods SET state=$2,updated_at=NOW() WHERE object_id=$1", p.id, state); err != nil {
				return false, err
			}
			if skipped && p.native.ID != "" {
				if err = w.queuePeriodOperation(ctx, c, p.id, p.revision, "update", p.current, tasksync.Snapshot{"archived": true}, fmt.Sprintf("skip:%s:%d", p.id, p.revision)); err != nil {
					return false, err
				}
			}
			if !skipped && !completed[p.native.Status] && done != "" {
				if err = w.queuePeriodOperation(ctx, c, p.id, p.revision, "update", p.current, tasksync.Snapshot{"status": done}, fmt.Sprintf("complete-state:%s:%d", p.id, p.revision)); err != nil {
					return false, err
				}
			}
			continue
		}
		if !completed[p.native.Status] || p.current["archived"] == true {
			continue
		}
		loc, _ := time.LoadLocation(c.Timezone)
		now := time.Now().In(loc)
		result, modelErr := recurrenceModel(ctx, c.Timezone, map[string]any{"mode": "progress", "task": parent, "today": now.Format("2006-01-02"), "now": now.Format(time.RFC3339), "date": p.date, "completion_date": now.Format("2006-01-02"), "done_status": done})
		if modelErr != nil {
			return false, modelErr
		}
		updates := tasksync.Snapshot{}
		for key, value := range result.Updates {
			if tasksync.Fields[key] {
				updates[key] = value
			}
		}
		var revision int64
		if err = w.Pool.QueryRow(ctx, "SELECT revision FROM sync_objects WHERE id=$1", series).Scan(&revision); err != nil {
			return false, err
		}
		if err = w.queuePeriodOperation(ctx, c, series, revision, "update", parent, updates, fmt.Sprintf("complete:%s:%d", p.id, p.revision)); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
