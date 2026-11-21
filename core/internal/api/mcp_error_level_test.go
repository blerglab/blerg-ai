package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

func TestMCPErrorLogLevelsAndPrivacy(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	oldLogger := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	old := failurelog.Default
	failurelog.Default = failurelog.New(logger)
	t.Cleanup(func() { failurelog.Default = old })
	for _, err := range []error{mcpconn.ErrNotFound, &mcpconn.ValidationError{Msg: "synthetic-private-validation"}, errors.New("synthetic-private-internal")} {
		writeMCPError(httptest.NewRecorder(), err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"level":"INFO"`) || !strings.Contains(lines[1], `"level":"INFO"`) || !strings.Contains(lines[2], `"level":"ERROR"`) {
		t.Fatalf("4xx must log at info; internal failures at error: %s", logs.String())
	}
	if strings.Contains(logs.String(), "synthetic-private") {
		t.Fatal("raw error leaked")
	}
}
