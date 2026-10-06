// Package worker applies durable TaskNotes deliveries through the official Paca REST API.
package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/jackc/pgx/v5"
)

const PluginID = "com.selfcommand.tasknotes-webhook"
const Version = "0.1.0"
const Schema = "plugin_data_com_selfcommand_tasknotes_webhook"

type Worker struct {
	DB               *pgx.Conn
	API, Key, Secret string
	HTTP             *http.Client
}
type apiError struct{ Code int }
type associationConflict struct{ message string }

func (e associationConflict) Error() string { return e.message }

func (e apiError) Error() string { return fmt.Sprintf("Paca API HTTP %d", e.Code) }
func readSecret(name string) (string, error) {
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return "", fmt.Errorf("%s_FILE required", name)
	}
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}
func New(ctx context.Context) (*Worker, error) {
	key, err := readSecret("PACA_API_KEY")
	if err != nil || key == "" {
		return nil, errors.New("PACA_API_KEY_FILE required")
	}
	secret, err := readSecret("WORKER_SECRET")
	if err != nil || len(secret) != 64 {
		return nil, errors.New("WORKER_SECRET_FILE required")
	}
	base := strings.TrimRight(os.Getenv("PACA_API_URL"), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("valid fixed PACA_API_URL required")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["search_path"] = Schema
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Worker{db, base, key, secret, &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (w *Worker) control(ctx context.Context) error {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	nonce := hex.EncodeToString(b)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(w.Secret))
	mac.Write([]byte("GET\n/worker/control\n" + stamp + "\n" + nonce))
	req, err := http.NewRequestWithContext(ctx, "GET", w.API+"/api/v1/plugins/"+PluginID+"/worker/control", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Worker-Timestamp", stamp)
	req.Header.Set("X-Worker-Nonce", nonce)
	req.Header.Set("X-Worker-Signature", hex.EncodeToString(mac.Sum(nil)))
	r, err := w.HTTP.Do(req)
	if err != nil {
		return errors.New("host control unavailable")
	}
	defer r.Body.Close()
	var c struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Schema  int    `json:"schema_version"`
		Enabled bool   `json:"enabled"`
	}
	if r.StatusCode != 200 || json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&c) != nil || !c.Enabled || c.ID != PluginID || c.Version != Version || c.Schema != 3 {
		return errors.New("host disabled or worker version/schema mismatch")
	}
	return nil
}
func (w *Worker) call(ctx context.Context, method, path string, body any, out any) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.API+"/api/v1"+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.Key)
	req.Header.Set("Content-Type", "application/json")
	r, err := w.HTTP.Do(req)
	if err != nil {
		return errors.New("Paca API response unknown")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return apiError{r.StatusCode}
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 8*1024*1024)).Decode(&envelope); err != nil {
		return errors.New("Paca API response unknown")
	}
	return json.Unmarshal(envelope.Data, out)
}

type connection struct {
	ID, Project, Timezone string
	Statuses              map[string]string
	Priorities            map[string]int
	Revision              int
}
type source struct {
	ID                       int64
	TaskID, Ref, State, Hash string
	Version                  *time.Time
	Tags                     []string
}
type task struct {
	ID     string         `json:"id"`
	Tags   []string       `json:"tags"`
	Custom map[string]any `json:"custom_fields"`
}

func (w *Worker) Tick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := w.control(ctx); err != nil {
		return err
	}
	var connectionID string
	err := w.DB.QueryRow(ctx, "SELECT i.connection_id::text FROM inbox i JOIN connections c ON c.id=i.connection_id WHERE c.enabled AND i.state IN ('pending','error','uncertain') AND i.next_attempt<=NOW() AND (i.lease_until IS NULL OR i.lease_until<NOW()) ORDER BY i.id LIMIT 1").Scan(&connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var locked bool
	if err = w.DB.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", connectionID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		_, _ = w.DB.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1,0))", connectionID)
	}()
	c := connection{ID: connectionID}
	var sm, pm []byte
	err = w.DB.QueryRow(ctx, "SELECT project_id::text,timezone,status_map,priority_map,revision FROM connections WHERE id=$1 AND enabled", connectionID).Scan(&c.Project, &c.Timezone, &sm, &pm, &c.Revision)
	if err != nil {
		return err
	}
	if json.Unmarshal(sm, &c.Statuses) != nil || json.Unmarshal(pm, &c.Priorities) != nil {
		return errors.New("invalid mapping")
	}
	var id int64
	var raw []byte
	lease := make([]byte, 24)
	if _, err = rand.Read(lease); err != nil {
		return err
	}
	owner := hex.EncodeToString(lease)
	err = w.DB.QueryRow(ctx, "UPDATE inbox SET lease_owner=$2,lease_until=NOW()+INTERVAL '60 seconds' WHERE id=(SELECT id FROM inbox WHERE connection_id=$1 AND state IN ('pending','error','uncertain') AND next_attempt<=NOW() AND (lease_until IS NULL OR lease_until<NOW()) ORDER BY id LIMIT 1) RETURNING id,body", connectionID, owner).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer releaseCancel()
		_, _ = w.DB.Exec(releaseCtx, "UPDATE inbox SET lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2", id, owner)
	}()
	e, err := tasknotes.Decode(raw)
	if err != nil {
		return w.finish(ctx, id, "conflict", "invalid stored envelope")
	}
	s, err := w.resolve(ctx, c, e)
	if err != nil {
		var conflict associationConflict
		if errors.As(err, &conflict) {
			return w.finish(ctx, id, "conflict", conflict.Error())
		}
		return err
	}
	version := e.Version()
	hash := e.SnapshotHash()
	if s.Version != nil {
		if version.Before(*s.Version) {
			return w.finish(ctx, id, "stale", "older source version ignored")
		}
		if version.Equal(*s.Version) {
			if hash == s.Hash {
				return w.finish(ctx, id, "applied", "")
			}
			return w.finish(ctx, id, "conflict", "same source timestamp has different contents")
		}
	}
	if version.After(time.Now().Add(5 * time.Minute)) {
		return w.finish(ctx, id, "conflict", "source clock is ahead; correct clock and resolve")
	}
	if s.State == "deleted" {
		return w.finish(ctx, id, "conflict", "source tombstoned; explicit new association required")
	}
	if s.State == "creating" || s.State == "uncertain" {
		found, err := w.findRef(ctx, c.Project, s.Ref)
		if err != nil {
			return w.finish(ctx, id, "uncertain", err.Error())
		}
		if len(found) != 1 {
			message := "creation result unknown; manual verification required"
			if len(found) > 1 {
				message = "multiple tasks share external marker"
			}
			return w.finish(ctx, id, "uncertain", message)
		}
		s.TaskID = found[0].ID
		s.State = "linked"
		_, err = w.DB.Exec(ctx, "UPDATE sources SET paca_task_id=$1,state='linked' WHERE id=$2", s.TaskID, s.ID)
		if err != nil {
			return err
		}
	}
	root := "/projects/" + c.Project + "/tasks"
	var current task
	if s.TaskID != "" {
		err = w.call(ctx, "GET", root+"/"+s.TaskID, nil, &current)
		if err != nil {
			var ae apiError
			if e.Event == "task.deleted" && errors.As(err, &ae) && ae.Code == 404 {
				return w.applied(ctx, id, s, e, "deleted")
			}
			return w.finish(ctx, id, "conflict", "mapped task missing or inaccessible; verify association")
		}
		if s.State == "link_pending" {
			s.State = "linked"
			if _, err = w.DB.Exec(ctx, "UPDATE sources SET state='linked' WHERE id=$1", s.ID); err != nil {
				return err
			}
		}
	}
	if e.Event == "task.deleted" {
		if s.TaskID != "" {
			err = w.call(ctx, "DELETE", root+"/"+s.TaskID, nil, nil)
			var ae apiError
			if err != nil && !(errors.As(err, &ae) && ae.Code == 404) {
				return w.finish(ctx, id, "error", err.Error())
			}
		}
		return w.applied(ctx, id, s, e, "deleted")
	}
	payload, err := w.payload(ctx, c, e, s, current)
	if err != nil {
		return w.finish(ctx, id, "conflict", err.Error())
	}
	// Recheck connection enable/revision immediately before an external mutation.
	var enabled bool
	var revision int
	if err = w.DB.QueryRow(ctx, "SELECT enabled,revision FROM connections WHERE id=$1", c.ID).Scan(&enabled, &revision); err != nil {
		return err
	}
	if !enabled || revision != c.Revision {
		return errors.New("connection configuration changed; paused")
	}
	if s.TaskID == "" {
		// Persist intent before POST. Any crash after this point is reconciled, never blindly retried.
		if _, err = w.DB.Exec(ctx, "UPDATE sources SET state='creating' WHERE id=$1", s.ID); err != nil {
			return err
		}
		var created task
		err = w.call(ctx, "POST", root, payload, &created)
		if err != nil || created.ID == "" {
			var ae apiError
			if errors.As(err, &ae) && (ae.Code == 400 || ae.Code == 401 || ae.Code == 403 || ae.Code == 404 || ae.Code == 422) {
				_, _ = w.DB.Exec(ctx, "UPDATE sources SET state='new' WHERE id=$1", s.ID)
				return w.finish(ctx, id, "error", err.Error())
			}
			_, _ = w.DB.Exec(ctx, "UPDATE sources SET state='uncertain' WHERE id=$1", s.ID)
			return w.finish(ctx, id, "uncertain", "creation result unknown; verifying external marker")
		}
		s.TaskID = created.ID
		if _, err = w.DB.Exec(ctx, "UPDATE sources SET paca_task_id=$1,state='linked' WHERE id=$2", s.TaskID, s.ID); err != nil {
			return err
		}
	} else if err = w.call(ctx, "PATCH", root+"/"+s.TaskID, payload, nil); err != nil {
		return w.finish(ctx, id, "error", err.Error())
	}
	return w.applied(ctx, id, s, e, "linked")
}
func (w *Worker) resolve(ctx context.Context, c connection, e tasknotes.Envelope) (source, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return source{}, err
	}
	defer tx.Rollback(ctx)
	path := e.Data.Task.Path
	var sid int64
	err = tx.QueryRow(ctx, "SELECT source_id FROM path_aliases WHERE connection_id=$1 AND vault_key=$2 AND path=$3", c.ID, e.VaultKey(), path).Scan(&sid)
	if err == nil && e.Data.Previous != nil && e.Data.Previous.Path != path {
		var oldID int64
		oldErr := tx.QueryRow(ctx, "SELECT source_id FROM path_aliases WHERE connection_id=$1 AND vault_key=$2 AND path=$3", c.ID, e.VaultKey(), e.Data.Previous.Path).Scan(&oldID)
		if oldErr == nil && oldID != sid {
			return source{}, associationConflict{"rename would merge two sources; manual resolution required"}
		}
		if oldErr != nil && !errors.Is(oldErr, pgx.ErrNoRows) {
			return source{}, oldErr
		}
	}
	if errors.Is(err, pgx.ErrNoRows) && e.Data.Previous != nil && e.Data.Previous.Path != path {
		err = tx.QueryRow(ctx, "SELECT source_id FROM path_aliases WHERE connection_id=$1 AND vault_key=$2 AND path=$3", c.ID, e.VaultKey(), e.Data.Previous.Path).Scan(&sid)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		b := make([]byte, 16)
		if _, err = rand.Read(b); err != nil {
			return source{}, err
		}
		ref := "tasknotes:" + hex.EncodeToString(b)
		err = tx.QueryRow(ctx, "INSERT INTO sources(connection_id,vault_key,source_key,external_ref) VALUES($1,$2,$3,$4) RETURNING id", c.ID, e.VaultKey(), path, ref).Scan(&sid)
	}
	if err != nil {
		return source{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", c.ID, e.VaultKey(), path, sid); err != nil {
		return source{}, err
	}
	var s source
	var tags []byte
	err = tx.QueryRow(ctx, "SELECT id,COALESCE(paca_task_id::text,''),external_ref,state,version_at,snapshot_hash,source_tags FROM sources WHERE id=$1", sid).Scan(&s.ID, &s.TaskID, &s.Ref, &s.State, &s.Version, &s.Hash, &tags)
	if err != nil {
		return s, err
	}
	_ = json.Unmarshal(tags, &s.Tags)
	return s, tx.Commit(ctx)
}
func (w *Worker) findRef(ctx context.Context, project, ref string) ([]task, error) {
	found := []task{}
	cursor := ""
	for pages := 0; pages < 10000; pages++ {
		var list struct {
			Items []task  `json:"items"`
			Next  *string `json:"next_cursor"`
		}
		path := "/projects/" + project + "/tasks?page_size=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		if err := w.call(ctx, "GET", path, nil, &list); err != nil {
			return nil, err
		}
		for _, t := range list.Items {
			if t.Custom["_integration_ref_v1"] == ref {
				found = append(found, t)
			}
		}
		if list.Next == nil || *list.Next == "" {
			return found, nil
		}
		if *list.Next == cursor {
			return nil, errors.New("pagination did not advance")
		}
		cursor = *list.Next
	}
	return nil, errors.New("reconciliation limit reached")
}
func (w *Worker) payload(ctx context.Context, c connection, e tasknotes.Envelope, s source, current task) (map[string]any, error) {
	t := e.Data.Task
	start, err := tasknotes.ParseDate(t.Scheduled, c.Timezone)
	if err != nil {
		return nil, err
	}
	due, err := tasknotes.ParseDate(t.Due, c.Timezone)
	if err != nil {
		return nil, err
	}
	priority := 35
	if v, ok := c.Priorities[t.Priority]; ok {
		priority = v
	}
	owned := map[string]bool{}
	for _, tag := range s.Tags {
		owned[tag] = true
	}
	tags := []string{}
	seen := map[string]bool{}
	for _, tag := range current.Tags {
		if !owned[tag] && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	for _, tag := range t.Tags {
		if !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	metadata := map[string]any{"version": 1, "source": "tasknotes", "archived": t.Archived || e.Event == "task.archived", "recurring": t.Recurrence != nil, "timezone": c.Timezone, "start_precision": start.Precision, "due_precision": due.Precision, "start_instant": start.Value, "due_instant": due.Value}
	payload := map[string]any{"title": t.Title, "start_date": start.Value, "due_date": due.Value, "importance": priority, "tags": tags, "custom_fields": map[string]any{"_integration_ref_v1": s.Ref, "_integration_state_v1": metadata}}
	var statuses struct {
		Items []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err = w.call(ctx, "GET", "/projects/"+c.Project+"/task-statuses", nil, &statuses); err != nil {
		return nil, err
	}
	mapped := c.Statuses[t.Status]
	completed := e.Event == "task.completed" || strings.EqualFold(t.Status, "done") || strings.EqualFold(t.Status, "completed")
	for _, st := range statuses.Items {
		if mapped != "" && st.ID == mapped {
			payload["status_id"] = mapped
			return payload, nil
		}
		if mapped == "" && ((completed && st.Category == "done") || (!completed && st.Category == "todo")) {
			payload["status_id"] = st.ID
			return payload, nil
		}
	}
	if mapped != "" || completed {
		return nil, errors.New("configure a status belonging to this project")
	}
	return payload, nil
}
func (w *Worker) finish(ctx context.Context, id int64, state, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := w.DB.Exec(ctx, "UPDATE inbox SET state=$1,error=$2,attempts=attempts+1,next_attempt=NOW()+LEAST(300,POWER(2,LEAST(attempts+1,8))) * INTERVAL '1 second',updated_at=NOW() WHERE id=$3", state, message, id)
	return err
}
func (w *Worker) applied(ctx context.Context, id int64, s source, e tasknotes.Envelope, state string) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tags, _ := json.Marshal(e.Data.Task.Tags)
	_, err = tx.Exec(ctx, "UPDATE sources SET state=$1,version_at=$2,snapshot_hash=$3,source_tags=$4::jsonb,updated_at=NOW() WHERE id=$5", state, e.Version(), e.SnapshotHash(), string(tags), s.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE inbox SET state='applied',error='',updated_at=NOW() WHERE id=$1", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
