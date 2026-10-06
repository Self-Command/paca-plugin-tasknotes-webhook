package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDisabledOrMismatchedHostPreventsMutation(t *testing.T) {
	for _, mode := range []string{"disabled", "version", "schema", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/plugins/"+PluginID+"/worker/control" {
					mutations++
					w.WriteHeader(201)
					return
				}
				if mode == "unavailable" {
					w.WriteHeader(404)
					return
				}
				version := Version
				schema := 3
				enabled := true
				if mode == "disabled" {
					enabled = false
				}
				if mode == "version" {
					version = "0.0.0"
				}
				if mode == "schema" {
					schema = 2
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": PluginID, "version": version, "schema_version": schema, "enabled": enabled})
			}))
			defer server.Close()
			worker := &Worker{API: server.URL, Key: "private-test-key", Secret: "test-worker-secret", HTTP: server.Client()}
			if err := worker.call(context.Background(), "POST", "/projects/project/tasks", map[string]string{"title": "no mutation"}, nil); err == nil || mutations != 0 {
				t.Fatalf("host %s must stop mutation: %v count=%d", mode, err, mutations)
			}
		})
	}
}
