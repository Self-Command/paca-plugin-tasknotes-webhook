// Package tasknotes defines the official 4.13.8 webhook envelope, without changing TaskNotes.
package tasknotes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
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
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		return e, errors.New("invalid event timestamp")
	}
	e.Data.Task.Path = NormalizePath(e.Data.Task.Path)
	if e.Data.Previous != nil {
		e.Data.Previous.Path = NormalizePath(e.Data.Previous.Path)
	}
	return e, nil
}
func NormalizePath(s string) string { return path.Clean(strings.ReplaceAll(s, "\\", "/")) }
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
func (e Envelope) Version() time.Time {
	// dateModified is not a monotonic TaskNotes revision; ties remain visible conflicts.
	if t, err := time.Parse(time.RFC3339Nano, e.Data.Task.DateModified); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	return t
}
func (e Envelope) SnapshotHash() string {
	kind := "live"
	if e.Event == "task.deleted" {
		kind = "deleted"
	}
	if e.Event == "task.archived" || e.Data.Task.Archived {
		kind = "archived"
	}
	b, _ := json.Marshal(e.Data.Task)
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
