package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasksync"
	"os"
	"os/exec"
	"time"
)

type recurrencePeriod struct {
	Date     string            `json:"date"`
	Snapshot tasksync.Snapshot `json:"snapshot"`
}
type recurrenceOutput struct {
	Version string             `json:"version"`
	Periods []recurrencePeriod `json:"periods"`
	Updates tasksync.Snapshot  `json:"updates"`
	Error   string             `json:"error"`
}

func recurrenceModel(ctx context.Context, timezone string, input map[string]any) (recurrenceOutput, error) {
	var output recurrenceOutput
	if _, err := time.LoadLocation(timezone); err != nil {
		return output, errors.New("任务时区无效。")
	}
	engine := os.Getenv("RECURRENCE_ENGINE")
	if engine == "" {
		engine = "/opt/recurrence/index.mjs"
		if _, err := os.Stat(engine); err != nil {
			engine = "recurrence/index.mjs"
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return output, err
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "node", "--max-old-space-size=64", engine)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TZ=" + timezone, "NODE_ENV=production"}
	cmd.Stdin = bytes.NewReader(raw)
	result, runErr := cmd.Output()
	if len(result) > 2*1024*1024 || json.Unmarshal(result, &output) != nil {
		return output, errors.New("循环排期暂时不可用。")
	}
	if output.Error != "" {
		return output, errors.New(output.Error)
	}
	if runErr != nil || output.Version != "0.3.0-rc.9" {
		return output, errors.New("循环模型版本不匹配。")
	}
	return output, nil
}
