package daemon

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/gorilla/websocket"
)

func TestDaemonFailureLogsSafeCause(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	err := &os.PathError{Op: "CANARY_SECRET", Path: "/CANARY_SECRET", Err: os.ErrPermission}
	e := &LocalExplorer{}
	e.diagnoseStatus(context.Background(), "Git status", err, "CANARY_SECRET")
	_ = workspaceFileError("CANARY_SECRET", err)
	_, _ = pinGitDir("/CANARY_SECRET", nil)
	h := &AgentHost{}
	_ = h.adoptOne(AgentRecord{WorkDir: "CANARY_SECRET"})
	for _, want := range []string{`"stage":"workspace status: Git status"`, `"stage":"workspace file read"`, `"cause":"permission"`, `"stage":"commit pin resolve workspace"`, `"stage":"adoption: record workspace"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal(out.String())
	}
}

func TestHelperFailureCodeCannotCarryContent(t *testing.T) {
	if safeFailureCode("CANARY_SECRET") != "unknown" || safeFailureCode("unavailable") != "unavailable" {
		t.Fatal("untrusted helper code accepted")
	}
}

func TestAdoptionScratchLogDoesNotExposeWorkspacePath(t *testing.T) {
	host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
	path := filepath.Join(filepath.Dir(project), ".scratch-CANARY_SECRET")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old, writer := failurelog.Default, log.Writer()
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	log.SetOutput(&out)
	defer func() { failurelog.Default = old; log.SetOutput(writer) }()
	host.adopting["paused"] = &adoptingSession{rec: AgentRecord{SessionID: "paused", Repo: filepath.Base(path), WorkDir: path}, failed: "recovery paused"}
	host.DropRecord("paused")
	if strings.Contains(out.String(), "CANARY_SECRET") || !strings.Contains(out.String(), "adoption scratch retained") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(path, "work.txt")); err != nil {
		t.Fatal("retained work lost", err)
	}
}

func TestAdoptionRefusalLogsEachSessionAndProtectsPrivateDetail(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	for _, id := range []string{"session-one", "session-two"} {
		logAdoptionRefusal(id, adoptWorkspaceStopping)
	}
	logAdoptionRefusal("private-detail", "spawn failed: CANARY_SECRET /private/workspace")
	logAdoptionRefusal("missing-workspace", "its workspace /CANARY_SECRET is gone")
	for _, want := range []string{`"session_id":"session-one"`, `"session_id":"session-two"`, `"reason":"its workspace is gone"`, `"cause":"safety_check"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "CANARY_SECRET") || strings.Contains(out.String(), "/private/workspace") {
		t.Fatal(out.String())
	}
}

func TestDaemonSocketCloseLogKeepsCodeWithoutPrivateText(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	logDaemonSocketFailure("read", &websocket.CloseError{Code: 1006, Text: "CANARY_SECRET"})
	logDaemonSocketFailure("limit", websocket.ErrReadLimit)
	for _, want := range []string{`"close_code":1006`, `"cause":"websocket_close"`, `"cause":"read_limit"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal("private close text disclosed")
	}
}
