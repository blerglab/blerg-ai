package cron

import (
	"github.com/blerglab/blerg-ai/runner/internal/commandsteps"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
	"time"
)

func TestFixedCommandBudgetCannotExceedRuntime(t *testing.T) {
	steps := []commandsteps.Step{{Argv: []string{"echo"}, TimeoutSeconds: 61, MaxOutputBytes: 32}}
	d, _ := commandsteps.Approve(steps, commandsteps.Hash(steps), "person", time.Now())
	c := &db.Cron{Name: "health", Prompt: "report", Engine: "claude", Runtime: "docker", CommandSteps: d, MaxRuntimeSeconds: 60, MCP: []byte("[]"), TokenID: "fixture", TokenExpiresAt: time.Now().Add(time.Hour)}
	if err := validateFields(c, time.Now()); err == nil {
		t.Fatal("step budget exceeds runtime")
	}
}
