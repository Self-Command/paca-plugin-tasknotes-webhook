// Package tasknotes defines the official 4.13.8 webhook envelope, without changing TaskNotes.
package tasknotes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
)

type Task struct {
	ID           string   `json:"id"`
	Path         string   `json:"path"`
	Title        string   `json:"title"`
	Details      *string  `json:"details,omitempty"`
	Status       string   `json:"status"`
	Priority     string   `json:"priority"`
	Scheduled    string   `json:"scheduled"`
	Due          string   `json:"due"`
	Archived     bool     `json:"archived"`
	Tags         []string `json:"tags"`
	DateModified string   `json:"dateModified"`
	DateCreated  string   `json:"dateCreated,omitempty"`
	Recurrence   any      `json:"recurrence"`
}

func (t Task) Recurring() bool {
	if t.Recurrence == nil {
		return false
	}
	if value, ok := t.Recurrence.(string); ok {
		return strings.TrimSpace(value) != ""
	}
	return true
}

type Envelope struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	Vault     struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"vault"`
	Data struct {
		Task     Task  `json:"task"`
		Previous *Task `json:"previous,omitempty"`
	} `json:"data"`
}

func AllowedEvent(event string) bool {
	switch event {
	case "task.created", "task.updated", "task.completed", "task.deleted", "task.archived", "task.unarchived":
		return true
	}
	return false
}
func Decode(raw []byte) (Envelope, error) {
	var e Envelope
	if len(raw) > 1024*1024 || json.Unmarshal(raw, &e) != nil {
		return e, errors.New("invalid webhook JSON or size")
	}
	if !AllowedEvent(e.Event) || strings.TrimSpace(e.Data.Task.Path) == "" || strings.TrimSpace(e.Data.Task.Title) == "" || e.Vault.Name == "" {
		return e, errors.New("unsupported event or missing task/vault identity")
	}
	if len(e.Data.Task.Path) > 1024 || len([]rune(e.Data.Task.Title)) > 500 || len(e.Data.Task.Tags) > 128 || len(e.Vault.Name) > 256 || len(e.Vault.Path) > 4096 {
		return e, errors.New("task fields exceed size limits")
	}
	if e.Data.Task.Details != nil && len(*e.Data.Task.Details) > 262144 {
		return e, errors.New("task details exceed size limit")
	}
	for _, tag := range e.Data.Task.Tags {
		if len([]rune(tag)) > 128 {
			return e, errors.New("task tag exceeds size limit")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		return e, errors.New("invalid event timestamp")
	}
	e.Data.Task.Path = NormalizePath(e.Data.Task.Path)
	if !ValidPath(e.Data.Task.Path) {
		return e, errors.New("task path must be relative to the paired vault")
	}
	if e.Data.Previous != nil {
		e.Data.Previous.Path = NormalizePath(e.Data.Previous.Path)
		if e.Data.Previous.Path != "." && !ValidPath(e.Data.Previous.Path) {
			return e, errors.New("invalid previous task path")
		}
	}
	return e, nil
}
func NormalizePath(s string) string { return path.Clean(strings.ReplaceAll(s, "\\", "/")) }
func ValidPath(s string) bool {
	return len(s) <= 1024 && s != "." && !strings.HasPrefix(s, "/") && s != ".." && !strings.HasPrefix(s, "../") && !strings.Contains(s, ":") && CleanText(s) == strings.TrimSpace(s)
}
func CleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}
func NormalizeTags(tags []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range tags {
		tag := strings.TrimPrefix(CleanText(value), "#")
		if tag != "" && !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	return result
}
func ValidSignature(raw []byte, secret, signature string) bool {
	received, err := hex.DecodeString(signature)
	if err != nil || len(received) != 32 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return hmac.Equal(received, mac.Sum(nil))
}
func Hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

// A paired connection identifies the vault. Its machine-specific absolute path
// must not create a second task when the same vault moves to another computer.
func (e Envelope) VaultKey() string { return "connection" }

// Event time and task modification time are independent, neither is a global revision.
func (e Envelope) Version() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	return t
}
func (e Envelope) TaskModified() *time.Time {
	t, err := time.Parse(time.RFC3339Nano, e.Data.Task.DateModified)
	if err != nil {
		return nil
	}
	return &t
}

// EffectiveTask gives explicit lifecycle events precedence over cached archived flags.
func (e Envelope) EffectiveTask() Task {
	t := e.Data.Task
	if e.Event == "task.archived" {
		t.Archived = true
	}
	if e.Event == "task.unarchived" {
		t.Archived = false
	}
	return t
}

// CanonicalHash excludes volatile timestamps and path-derived identity, never title alone.
func CanonicalHash(t Task, ignoreArchived bool) string {
	if t.Details!=nil && *t.Details=="" { t.Details=nil }
 t.ID = ""
	t.Path = ""
	t.DateModified = ""
	if !t.Recurring() {
		t.Recurrence = nil
	}
	t.Tags = NormalizeTags(t.Tags)
	sort.Strings(t.Tags)
	if ignoreArchived {
		t.Archived = false
	}
	b, _ := json.Marshal(t)
	return Hash(b)
}
func (e Envelope) SnapshotHash() string {
	kind := "live"
	if e.Event == "task.deleted" {
		kind = "deleted"
	}
	return Hash([]byte(kind + "\n" + CanonicalHash(e.EffectiveTask(), false)))
}

// Archive moves keep the filename and birth time; remove only the configured archive tag.
func ArchiveEquivalent(a, b Task, archiveTag string) bool {
	if path.Base(a.Path) != path.Base(b.Path) {
		return false
	}
	strip := func(t Task) Task {
		t.Archived = false
		tags := []string{}
		for _, tag := range NormalizeTags(t.Tags) {
			if tag != strings.TrimPrefix(archiveTag, "#") {
				tags = append(tags, tag)
			}
		}
		t.Tags = tags
		return t
	}
	return CanonicalHash(strip(a), false) == CanonicalHash(strip(b), false)
}

// Used only to verify the old accepted inbox during a schema-3 upgrade.
func (e Envelope) LegacySnapshotHash() string {
	kind := "live"
	if e.Event == "task.deleted" {
		kind = "deleted"
	}
	if e.Event == "task.archived" || e.Data.Task.Archived {
		kind = "archived"
	}
	legacy := e.Data.Task
	legacy.DateCreated = ""
	b, _ := json.Marshal(legacy)
	return Hash(append([]byte(kind+"\n"), b...))
}

type Date struct {
	Value     any
	Precision string
}

// Paca v0.18.6 stores native task dates as SQL DATE. Preserve the calendar
// date in that field and keep the full instant in versioned metadata.
func CoreDate(d Date, zone string) (any, string, error) {
	if d.Value == nil {
		return nil, "", nil
	}
	s, ok := d.Value.(string)
	if !ok {
		return nil, "", errors.New("invalid instant")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, "", err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, "", err
	}
	day := t.In(loc).Format("2006-01-02")
	return day + "T00:00:00Z", day, nil
}

func ParseDate(s, zone string) (Date, error) {
	if s == "" {
		return Date{nil, "missing"}, nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Date{}, errors.New("invalid timezone")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return Date{t.UTC().Format(time.RFC3339Nano), "instant"}, nil
	}
	if len(s) == 10 {
		_, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return Date{}, err
		}
		return Date{nil, "day"}, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			wall, parseErr := time.Parse(layout, s)
			if parseErr != nil || t.In(loc).Format("2006-01-02T15:04:05.999999999") != wall.Format("2006-01-02T15:04:05.999999999") {
				continue
			}
			// A local wall time can occur twice during DST. Require an offset in that case.
			for _, delta := range []time.Duration{-2 * time.Hour, -time.Hour, time.Hour, 2 * time.Hour} {
				if t.Add(delta).In(loc).Format("2006-01-02T15:04:05.999999999") == wall.Format("2006-01-02T15:04:05.999999999") {
					return Date{}, errors.New("ambiguous local time; supply UTC offset")
				}
			}
			return Date{t.UTC().Format(time.RFC3339Nano), "instant"}, nil
		}
	}
	return Date{}, errors.New("invalid local time or missing UTC offset")
}
