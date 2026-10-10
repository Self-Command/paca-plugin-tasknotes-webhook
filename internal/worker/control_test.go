package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/buildinfo"
)

func TestDisabledOrMismatchedHostPreventsMutation(t *testing.T) {
	for _, mode := range []string{"disabled", "version", "schema", "source", "identity", "unavailable"} {
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
				source := buildinfo.SourceSHA
				identity := PluginID
				schema := 8
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
				if mode == "source" {
					source = "0000000000000000000000000000000000000000"
				}
				if mode == "identity" {
					identity = "com.other.plugin"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": identity, "version": version, "source_sha": source, "schema_version": schema, "enabled": enabled})
			}))
			defer server.Close()
			worker := &Worker{API: server.URL, Key: "private-test-key", Secret: "test-worker-secret", HTTP: server.Client()}
			if err := worker.call(context.Background(), "POST", "/projects/project/tasks", map[string]string{"title": "no mutation"}, nil); err == nil || mutations != 0 {
				t.Fatalf("host %s must stop mutation: %v count=%d", mode, err, mutations)
			}
		})
	}
}

func TestHealthyHostUsesPersonalAPIKeyHeader(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/plugins/"+PluginID+"/worker/control" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": PluginID, "version": Version, "source_sha": buildinfo.SourceSHA, "schema_version": 8, "enabled": true})
			return
		}
		if r.Header.Get("X-API-Key") != "private-test-key" || r.Header.Get("Authorization") != "" {
			t.Error("personal API keys require X-API-Key, not JWT Bearer")
		}
		mutations++
		w.WriteHeader(201)
	}))
	defer server.Close()
	w := &Worker{API: server.URL, Key: "private-test-key", Secret: "test-worker-secret", HTTP: server.Client()}
	if err := w.call(context.Background(), "POST", "/projects/project/tasks", map[string]string{"title": "authenticated mutation"}, nil); err != nil || mutations != 1 {
		t.Fatalf("healthy host must allow authorized mutation: %v count=%d", err, mutations)
	}
}
