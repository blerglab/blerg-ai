package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestStartedBroadcastIncludesWorkspaceMembers(t *testing.T) {
	for _, runtime := range []string{"daemon", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			f := newPrivFixture(t)
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime=$2 WHERE id=$1`, f.pubSession, runtime); err != nil {
				t.Fatal(err)
			}
			manifestRuntime := "host"
			if runtime == "cluster" {
				manifestRuntime = "cluster"
			}
			manifest := fmt.Sprintf(`{"version":2,"runtime":%q,"owner_id":"acct-a","members":[{"id":"one","name":"app","path":"app"}]}`, manifestRuntime)
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO session_workspaces(session_id,launch_generation,manifest,results) VALUES($1,'fixture',$2,'[]')`, f.pubSession, manifest); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetSession(t.Context(), f.pool, f.pubSession)
			if err != nil {
				t.Fatal(err)
			}
			if info := f.hub.sessionInfoFor(t.Context(), *row, privAcctA); info.Workspace == nil {
				t.Fatal("fixture workspace not visible to owner")
			}
			if info := f.hub.sessionInfoFor(t.Context(), *row, privAcctB); info.Workspace != nil {
				t.Fatal("non-owner received workspace")
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/ws/browser", f.hub.ServeBrowser(f.pool, f.api.AuthorizeBrowserWS))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			owner := f.dial(t, srv, f.tok(t, "human", privAcctA, coreAuthBrowserCap))
			owner.until(t, "initial_state")
			other := f.dial(t, srv, f.tok(t, "human", privAcctB, coreAuthBrowserCap))
			other.until(t, "initial_state")
			ctx := t.Context()
			if runtime == "cluster" {
				raw, _, err := db.MintRuntimeLaunch(ctx, f.pool, f.pubSession, "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				token, err := db.ValidateRuntimeToken(ctx, f.pool, raw)
				if err != nil {
					t.Fatal(err)
				}
				f.dc.RuntimeTokenID, f.dc.BoundSessionID = token.ID, f.pubSession
				ctx = db.WithRuntimeAuthority(ctx, token.ID)
			}
			HandleSessionStarted(ctx, f.hub, f.pool, f.dc.ID, protocol.SessionStarted{Type: "session_started", SessionID: f.pubSession, Kind: "agent"})
			frames := owner.until(t, `"type":"session_started"`)
			var got protocol.BrowserSessionStarted
			if err := json.Unmarshal([]byte(frames[len(frames)-1]), &got); err != nil {
				t.Fatal(err)
			}
			if got.Session.Workspace == nil || len(got.Session.Workspace.Members) != 1 || got.Session.Workspace.Members[0].Name != "app" {
				t.Fatal("started snapshot omitted workspace member")
			}
			f.hub.BroadcastJSON(map[string]string{"type": "review_test_marker"})
			for _, frame := range other.until(t, "review_test_marker") {
				if strings.Contains(frame, `"workspace"`) {
					t.Fatal("non-owner received workspace snapshot")
				}
			}
		})
	}
}
