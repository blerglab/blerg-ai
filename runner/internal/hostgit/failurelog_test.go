package hostgit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

func TestFailureLogExcludesGitArgumentsAndDirectory(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = Run(ctx, Options{Dir: "/CANARY_SECRET"}, "show", "CANARY_SECRET")
	if strings.Contains(out.String(), "CANARY_SECRET") || !strings.Contains(out.String(), `"operation":"show"`) || !strings.Contains(out.String(), `"cause":"canceled"`) {
		t.Fatal(out.String())
	}
}
