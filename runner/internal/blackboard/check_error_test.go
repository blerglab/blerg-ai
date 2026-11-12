package blackboard

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

func TestMissingBoardReaderNamesAndLogsCause(t *testing.T) {
	var logs bytes.Buffer
	before := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = before })
	_, err := (Service{}).Releases(t.Context(), "CANARY_PRIVATE_BOARD")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "board.release_reader: not configured") {
		t.Fatalf("missing dependency cause: %v", err)
	}
	if !strings.Contains(logs.String(), `"cause":"board_release_reader_missing"`) {
		t.Fatalf("missing typed cause: %s", logs.String())
	}
	if strings.Contains(logs.String()+err.Error(), "CANARY_PRIVATE_BOARD") {
		t.Fatal("private board identifier leaked")
	}
}
