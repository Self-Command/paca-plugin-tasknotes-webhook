package main

import (
	"context"
	"encoding/json"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/buildinfo"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/worker"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"plugin": worker.PluginID, "version": worker.Version, "source_sha": buildinfo.SourceSHA, "phase": "tasknotes-integration"})
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w, err := worker.New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer w.DB.Close(context.Background())
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err = w.Tick(ctx); err != nil {
				log.Printf("processing paused: %v", err)
			}
		}
	}
}
