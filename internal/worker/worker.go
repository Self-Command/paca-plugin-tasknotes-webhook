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

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/buildinfo"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/jackc/pgx/v5"
)

const PluginID = "com.selfcommand.tasknotes-webhook"
const Version = buildinfo.Version
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
		Source  string `json:"source_sha"`
	}
	if r.StatusCode != 200 || json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&c) != nil || !c.Enabled || c.ID != PluginID || c.Version != Version || c.Schema != 4 || len(buildinfo.SourceSHA) != 40 || c.Source != buildinfo.SourceSHA {
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
	req.Header.Set("X-API-Key", w.Key)
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
 ArchiveTag string
	Statuses              map[string]string
	Priorities            map[string]int
	Revision              int
}
type source struct {
	ID                       int64
	TaskID, Ref, State, Hash string
	Version                  *time.Time
	Tags                     []string
	Snapshot                 *tasknotes.Task
	EventAt                  *time.Time
	LastEvent                string
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
	err = w.DB.QueryRow(ctx, "SELECT project_id::text,timezone,status_map,priority_map,revision,archive_tag FROM connections WHERE id=$1 AND enabled", connectionID).Scan(&c.Project, &c.Timezone, &sm, &pm, &c.Revision, &c.ArchiveTag)
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
	if err=w.hydrateLegacy(ctx,c.ID);err!=nil { return err }
 s, err := w.resolve(ctx, c, e)
	if err != nil {
		var conflict associationConflict
		if errors.As(err, &conflict) {
			return w.finish(ctx, id, "conflict", conflict.Error())
		}
		return err
	}
	decision, message := eventDecisionWithArchive(s, e, time.Now(), c.ArchiveTag)
	if decision != "apply" {
		if decision == "duplicate" {
			return w.applied(ctx, id, s, e, s.State)
		}
		return w.finish(ctx, id, decision, message)
	}
	if s.State == "unassociated" && e.Event != "task.created" && e.Event != "task.deleted" {
		return w.finish(ctx, id, "conflict", "unknown source path; verify rename or missed creation and associate manually")
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
func sourceForPath(ctx context.Context, tx pgx.Tx, connectionID, path string) (int64, error) {
	rows, err := tx.Query(ctx, "SELECT DISTINCT source_id FROM path_aliases WHERE connection_id=$1 AND path=$2", connectionID, path)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var id int64
	count := 0
	for rows.Next() {
		if err = rows.Scan(&id); err != nil {
			return 0, err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, pgx.ErrNoRows
	}
	if count != 1 {
		return 0, associationConflict{"legacy vault paths map to multiple tasks; manual resolution required"}
	}
	return id, nil
}
func (w *Worker) resolve(ctx context.Context, c connection, e tasknotes.Envelope) (source, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return source{}, err
	}
	defer tx.Rollback(ctx)
	path := e.Data.Task.Path
	var sid int64
	sid, err = sourceForPath(ctx, tx, c.ID, path)
	if err == nil && e.Data.Previous != nil && e.Data.Previous.Path != path {
		var oldID int64
		oldID, oldErr := sourceForPath(ctx, tx, c.ID, e.Data.Previous.Path)
		if oldErr == nil && oldID != sid {
			return source{}, associationConflict{"rename would merge two sources; manual resolution required"}
		}
		if oldErr != nil && !errors.Is(oldErr, pgx.ErrNoRows) {
			return source{}, oldErr
		}
	}
	if errors.Is(err, pgx.ErrNoRows) && e.Data.Previous != nil && e.Data.Previous.Path != path {
		sid, err = sourceForPath(ctx, tx, c.ID, e.Data.Previous.Path)
	}
	if errors.Is(err, pgx.ErrNoRows) && (e.Event == "task.archived" || e.Event == "task.unarchived" || e.Data.Previous != nil) {
		candidate := e.Data.Task
		if e.Data.Previous != nil {
			candidate = *e.Data.Previous
		}
		rows, matchErr := tx.Query(ctx, "SELECT id,snapshot FROM sources WHERE connection_id=$1 AND state='linked' AND snapshot IS NOT NULL", c.ID)
		if matchErr != nil {
			return source{}, matchErr
		}
		matches := []int64{}
		for rows.Next() {
			var id int64
			var raw []byte
			var t tasknotes.Task
			if rows.Scan(&id, &raw) == nil && json.Unmarshal(raw, &t) == nil && ((e.Data.Previous!=nil && tasknotes.CanonicalHash(candidate,false)==tasknotes.CanonicalHash(t,false)) || (e.Data.Previous==nil && tasknotes.ArchiveEquivalent(candidate,t,c.ArchiveTag))) {
				matches = append(matches, id)
			}
		}
		rows.Close()
		if rows.Err() != nil {
			return source{}, rows.Err()
		}
		if len(matches) == 1 {
			sid = matches[0]
			err = nil
		}
		if len(matches) > 1 {
			return source{}, associationConflict{"multiple full snapshots match the moved task; manual association required"}
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		b := make([]byte, 16)
		if _, err = rand.Read(b); err != nil {
			return source{}, err
		}
		ref := "tasknotes:" + hex.EncodeToString(b)
		initialState := "new"
		if e.Event != "task.created" && e.Event != "task.deleted" {
			initialState = "unassociated"
		}
		err = tx.QueryRow(ctx, "INSERT INTO sources(connection_id,vault_key,source_key,external_ref,state) VALUES($1,$2,$3,$4,$5) RETURNING id", c.ID, e.VaultKey(), path, ref, initialState).Scan(&sid)
	}
	if err != nil {
		return source{}, err
	}
	if e.Event == "task.created" {
		var state string
		var closed *time.Time
		if err = tx.QueryRow(ctx, "SELECT state,event_at FROM sources WHERE id=$1", sid).Scan(&state, &closed); err != nil {
			return source{}, err
		}
		if state == "deleted" && closed != nil && e.Version().After(*closed) {
			b := make([]byte, 16)
			if _, err = rand.Read(b); err != nil {
				return source{}, err
			}
			var next int64
			if err = tx.QueryRow(ctx, "INSERT INTO sources(connection_id,vault_key,source_key,external_ref,state,generation) SELECT $1,$2,$3,$4,'new',COALESCE(MAX(generation),0)+1 FROM sources WHERE connection_id=$1 AND vault_key=$2 AND source_key=$3 RETURNING id", c.ID, e.VaultKey(), path, "tasknotes:"+hex.EncodeToString(b)).Scan(&next); err != nil {
				return source{}, err
			}
			if _, err = tx.Exec(ctx, "DELETE FROM path_aliases WHERE source_id=$1", sid); err != nil {
				return source{}, err
			}
			sid = next
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO path_aliases(connection_id,vault_key,path,source_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", c.ID, e.VaultKey(), path, sid); err != nil {
		return source{}, err
	}
	var s source
	var tags, snapshot []byte
	err = tx.QueryRow(ctx, "SELECT id,COALESCE(paca_task_id::text,''),external_ref,state,version_at,snapshot_hash,source_tags,snapshot,event_at,last_event FROM sources WHERE id=$1", sid).Scan(&s.ID, &s.TaskID, &s.Ref, &s.State, &s.Version, &s.Hash, &tags, &snapshot, &s.EventAt, &s.LastEvent)
	if len(snapshot) > 0 {
		var t tasknotes.Task
		if json.Unmarshal(snapshot, &t) == nil {
			s.Snapshot = &t
		}
	}
	if err == nil && s.Snapshot == nil && s.Hash != "" {
		rows, queryErr := tx.Query(ctx, "SELECT i.body FROM inbox i WHERE i.connection_id=$1 AND i.state='applied' AND i.body->'data'->'task'->>'path' IN (SELECT path FROM path_aliases WHERE source_id=$2) ORDER BY i.id DESC LIMIT 100", c.ID, sid)
		if queryErr != nil {
			return s, queryErr
		}
		var accepted *tasknotes.Envelope
		for rows.Next() {
			var body []byte
			if rows.Scan(&body) == nil {
				prior, decodeErr := tasknotes.Decode(body)
				if decodeErr == nil && prior.LegacySnapshotHash() == s.Hash {
					accepted = &prior
					break
				}
			}
		}
		rows.Close()
		if rows.Err() != nil {
			return s, rows.Err()
		}
		if accepted != nil {
			t := accepted.EffectiveTask()
			s.Snapshot = &t
			stamp := accepted.Version()
			s.EventAt = &stamp
			s.LastEvent = accepted.Event
			s.Hash = accepted.SnapshotHash()
			b, _ := json.Marshal(t)
			if _, err = tx.Exec(ctx, "UPDATE sources SET snapshot=$1::jsonb,event_at=$2,task_modified_at=$3,last_event=$4,snapshot_hash=$5 WHERE id=$6", string(b), stamp, accepted.TaskModified(), accepted.Event, s.Hash, sid); err != nil {
				return s, err
			}
		}
	}
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
	t := e.EffectiveTask()
	start, err := tasknotes.ParseDate(t.Scheduled, c.Timezone)
	if err != nil {
		return nil, err
	}
	due, err := tasknotes.ParseDate(t.Due, c.Timezone)
	if err != nil {
		return nil, err
	}
	if start.Value != nil && due.Value != nil {
		startTime, _ := time.Parse(time.RFC3339Nano, start.Value.(string))
		dueTime, _ := time.Parse(time.RFC3339Nano, due.Value.(string))
		if dueTime.Before(startTime) {
			return nil, errors.New("due time cannot precede start time")
		}
	}
	title := tasknotes.CleanText(t.Title)
	if title == "" || len([]rune(title)) > 500 {
		return nil, errors.New("title must contain 1–500 characters")
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
	for _, tag := range tasknotes.NormalizeTags(t.Tags) {
		if !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	startCore, startDay, err := tasknotes.CoreDate(start, c.Timezone)
	if err != nil {
		return nil, err
	}
	dueCore, dueDay, err := tasknotes.CoreDate(due, c.Timezone)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{"version": 2, "source": "tasknotes", "archived": t.Archived, "recurring": t.Recurring(), "timezone": c.Timezone, "start_precision": start.Precision, "due_precision": due.Precision, "start_instant": start.Value, "due_instant": due.Value, "start_core_date": startDay, "due_core_date": dueDay, "start_source": t.Scheduled, "due_source": t.Due}
	custom := map[string]any{}
	for key, value := range current.Custom {
		custom[key] = value
	}
	custom["_integration_ref_v1"] = s.Ref
	custom["_integration_state_v1"] = metadata
	payload := map[string]any{"title": title, "start_date": startCore, "due_date": dueCore, "importance": priority, "tags": tags, "custom_fields": custom}
	if t.Details != nil {
		blocks := []any{}
		for _, line := range strings.Split(*t.Details, "\n") {
			blocks = append(blocks, map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": strings.TrimSuffix(line, "\r"), "styles": map[string]any{}}}, "children": []any{}})
		}
		payload["description"] = blocks
	}
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
	if t.Archived {
		mapped = c.Statuses["@archived"]
		if mapped == "" {
			return nil, errors.New("configure @archived with this project's archive status UUID")
		}
	}
	if e.Event == "task.completed" && c.Statuses["@completed"] != "" {
		mapped = c.Statuses["@completed"]
	}
	completed := e.Event == "task.completed" || strings.EqualFold(t.Status, "done") || strings.EqualFold(t.Status, "completed")
	category := "todo"
	switch strings.ToLower(t.Status) {
	case "", "none", "open":
	case "in-progress":
		category = "inprogress"
	case "done", "completed":
		category = "done"
	default:
		if mapped == "" && !completed && !t.Archived {
			return nil, errors.New("unknown TaskNotes status; configure project status mapping")
		}
	}
	if completed {
		category = "done"
	}
	for _, st := range statuses.Items {
		if mapped != "" && st.ID == mapped {
			payload["status_id"] = mapped
			return payload, nil
		}
		if mapped == "" && st.Category == category {
			payload["status_id"] = st.ID
			return payload, nil
		}
	}
	return nil, errors.New("configure a status belonging to this project")
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
	tags, _ := json.Marshal(tasknotes.NormalizeTags(e.Data.Task.Tags))
	snapshot, _ := json.Marshal(e.EffectiveTask())
	_, err = tx.Exec(ctx, "UPDATE sources SET state=$1,version_at=GREATEST(version_at,$2),snapshot_hash=$3,source_tags=$4::jsonb,event_at=GREATEST(event_at,$2),task_modified_at=$6,snapshot=$7::jsonb,last_event=$8,updated_at=NOW() WHERE id=$5", state, e.Version(), e.SnapshotHash(), string(tags), s.ID, e.TaskModified(), string(snapshot), e.Event)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE inbox SET state='applied',error='',updated_at=NOW() WHERE id=$1", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
