package server

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBoardLinkRefusalLogsTypedCause(t *testing.T) {
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	w := httptest.NewRecorder()
	blackboardError(w, &blackboard.PlanLinkError{BoardID: "CANARY_PRIVATE_BOARD", Cause: "board_not_linked"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "link board CANARY_PRIVATE_BOARD first") {
		t.Fatalf("missing reason: %s", w.Body.String())
	}
	if !strings.Contains(logs.String(), `"cause":"board_not_linked"`) || strings.Contains(logs.String(), "CANARY_PRIVATE_BOARD") {
		t.Fatalf("unsafe diagnostic: %s", logs.String())
	}
}
