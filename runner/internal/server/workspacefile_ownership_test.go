package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceFileOwnershipRefusalsExplainRecovery(t *testing.T) {
	for _, state := range []string{"unassigned", "other desktop owner", "compatibility without historical access", "other session owner", "administrator on another person’s desktop session", "agent"} {
		t.Run(state, func(t *testing.T) {
			f := newPrivFixture(t)
			account, kind, code, guidance := privAcctA, "human", "desktop_ownership", "Your desktops"
			caps := []string{"session.start"}
			switch state {
			case "unassigned", "compatibility without historical access":
				_, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=$2 WHERE id=$1`, f.dc.ID, state != "unassigned")
				if err != nil {
					t.Fatal(err)
				}
			case "other desktop owner":
				if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2 WHERE id=$1`, f.dc.ID, privAcctB); err != nil {
					t.Fatal(err)
				}
			case "other session owner":
				account, code, guidance = privAcctB, "session_ownership", "publish"
			case "administrator on another person’s desktop session":
				account, code, guidance = privAcctB, "session_ownership", "publish"
				caps = append(caps, "account.manage")
			case "agent":
				kind, code, guidance = "agent", "person_required", "Sign in"
			}
			request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+f.pubSession+"/workspace-file?path=.agent-notes/report.md", nil)
			request.SetPathValue("id", f.pubSession)
			request.Header.Set("Authorization", "Bearer "+f.tok(t, kind, account, caps...))
			response := httptest.NewRecorder()
			f.api.HandleGetWorkspaceFile(response, request)
			if response.Code != 403 || workspaceProblemCode(t, response.Body.Bytes()) != code || !strings.Contains(response.Body.String(), guidance) || strings.Contains(strings.ToLower(response.Body.String()), "not found") {
				t.Fatalf("unclear refusal: %d %s", response.Code, response.Body)
			}
			if len(f.dc.send) != 0 {
				t.Fatal("refused read reached daemon")
			}
		})
	}
}

func TestWorkspaceFileReassignmentDuringReadExplainsEnrollment(t *testing.T) {
	f := newPrivFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+f.pubSession+"/workspace-file?path=README.md", nil)
	request.SetPathValue("id", f.pubSession)
	request.Header.Set("Authorization", "Bearer "+f.tok(t, "human", privAcctA, "session.start"))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.api.HandleGetWorkspaceFile(response, request) }()
	select {
	case raw := <-f.dc.send:
		var read protocol.ReadWorkspaceFile
		if err := json.Unmarshal(raw, &read); err != nil {
			t.Fatal(err)
		}
		if err := db.WithDesktopFence(t.Context(), f.pool, f.dc.ID, func(ctx context.Context) error {
			_, err := db.AssignDesktop(ctx, f.pool, f.dc.ID, privAcctB, 1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		f.hub.deliverWorkspaceFileResult(f.dc, protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: read.RequestID, SessionID: read.SessionID, OK: true, Path: read.Path, Size: 2, ContentB64: "aGk=", Mtime: time.Now().UTC().Format(time.RFC3339Nano)})
	case <-done:
		t.Fatalf("read refused before reassignment: %d", response.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("read not dispatched")
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("read did not finish")
	}
	if response.Code != 403 || workspaceProblemCode(t, response.Body.Bytes()) != "desktop_ownership" || !strings.Contains(response.Body.String(), "Your desktops") || strings.Contains(response.Body.String(), "content_b64") {
		t.Fatalf("reassignment lacked recovery guidance: %d %s", response.Code, response.Body)
	}
}
