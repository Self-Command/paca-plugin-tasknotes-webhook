package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
)

func TestPeriodFreezeMustBeExplicitAndScoped(t *testing.T) {
	for _, test := range []struct {
		name, body        string
		code              int
		frozen, wantError bool
	}{
		{"not configured", `{"enabled":false}`, 200, false, false},
		{"open", `{"enabled":true,"frozen":true}`, 200, true, false},
		{"future", `{"enabled":true,"frozen":false}`, 200, false, false},
		{"old worker", `{"enabled":true}`, 200, false, true},
		{"service unavailable", `{}`, 503, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/times/freeze" || r.Header.Get("Authorization") != "Bearer service" {
					t.Error("unscoped freeze query")
				}
				out.WriteHeader(test.code)
				out.Write([]byte(test.body))
			}))
			defer server.Close()
			w := Worker{CheckinURL: server.URL, CheckinSecret: "service", HTTP: server.Client()}
			frozen, err := w.periodFrozen(context.Background(), syncConfig{connection: connection{Project: "project"}}, "task")
			if frozen != test.frozen || (err != nil) != test.wantError {
				t.Fatalf("freeze=%v err=%v", frozen, err)
			}
		})
	}
}
func TestParentReferenceRequiresPathNotTitleGuess(t *testing.T) {
	for ref, path := range map[string]string{"[[Tasks/阅读]]": "Tasks/阅读.md", "[[Tasks/阅读|别名]]": "Tasks/阅读.md", "[阅读](<Tasks/%E9%98%85%E8%AF%BB.md>)": "Tasks/阅读.md", "[[../Secrets]]": "", "https://example.org/task": ""} {
		if actual := parentPath(ref); actual != path {
			t.Fatalf("reference %q produced %q", ref, actual)
		}
	}
}

func TestEveryScheduleEntryChecksFrozenTime(t *testing.T) {
 calls := 0
 server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
  calls++
  if r.URL.Path != "/internal/v1/times/freeze" { t.Error("freeze probe must not create an instance") }
  out.Write([]byte(`{"enabled":true,"frozen":true}`))
 }))
 defer server.Close()
 w := Worker{CheckinURL:server.URL, CheckinSecret:"service", HTTP:server.Client()}
 base := tasksync.Snapshot{"scheduled":"2026-10-09T08:33", "due":"2026-10-09T08:53", "recurrence":nil}
 for _, field := range []string{"scheduled","due","recurrence","recurrence_anchor"} {
  next := tasksync.Snapshot{}
  for k,v := range base { next[k] = v }
  next[field] = "changed"
  if !errors.Is(w.guardScheduleChange(context.Background(), syncConfig{connection:connection{Project:"project"}}, "task", base, next),errScheduleFrozen) { t.Fatal("frozen entry accepted",field) }
 }
 if err := w.guardScheduleChange(context.Background(),syncConfig{connection:connection{Project:"project"}},"task",base,base); err != nil { t.Fatal(err) }
 if calls != 4 { t.Fatal("unchanged writes must not probe or mutate check-in",calls) }
}
