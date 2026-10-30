package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChildDiscardArchivesAndRetainsHistory(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(map[bool]string{false: "delegate", true: "human"}[human], func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture", 12, 3, 0, 0))
			token := fx.parentTok
			if human {
				token = fx.human("owner-1")
			}
			before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
			code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
			if code != http.StatusOK {
				t.Fatalf("discard=%d, want200", code)
			}
			var archived *time.Time
			if err := fx.pool.QueryRow(t.Context(), `SELECT archived_at FROM sessions WHERE id=$1`, child).Scan(&archived); err != nil || archived == nil {
				t.Fatalf("archive not durable: %v", err)
			}
			row := fx.row(child)
			if row.EndedAt == nil || row.Status != "ended" {
				t.Fatal("discard did not stop child")
			}
			if got := childrenLiveGet(t, fx, "owner-1", before); got.Code != 200 {
				t.Fatal("discard did not invalidate live cache")
			}
			var live struct {
				Children []liveChild `json:"children"`
			}
			if err := json.Unmarshal(childrenLiveGet(t, fx, "owner-1", "").Body.Bytes(), &live); err != nil || len(live.Children) != 0 {
				t.Fatal("archived child remains live")
			}
			rows, err := db.ListVisibleChildLiveRows(t.Context(), fx.pool, fx.parent, "owner-1")
			if err != nil || len(rows) != 1 {
				t.Fatal("archive lost insights/history rows")
			}
			r := httptest.NewRequest("GET", "/api/sessions/"+fx.parent+"/insights", nil)
			r.SetPathValue("id", fx.parent)
			r.Header.Set("Authorization", "Bearer "+fx.human("owner-1"))
			w := httptest.NewRecorder()
			fx.api.HandleGetSessionInsights(w, r)
			var insights struct {
				StartedSessions struct {
					Count int `json:"count"`
				} `json:"started_sessions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &insights); err != nil || w.Code != 200 || insights.StartedSessions.Count != 1 {
				t.Fatal("archive lost started-session totals")
			}
			code, _, _ = fx.do("GET", childRoute(fx.parent, child, "/result"), fx.parentTok, nil, nil)
			if code != 200 {
				t.Fatal("archive removed direct child access")
			}
			code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "cannot revive hidden worker"}, nil)
			if code != http.StatusConflict {
				t.Fatalf("archived input accepted: %d", code)
			}
			blocked, err := db.DelegationResumeBlocked(t.Context(), fx.pool, child)
			if err != nil || !blocked {
				t.Fatal("archived child can be adopted or resumed")
			}
			code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
			if code != 200 {
				t.Fatal("discard is not idempotent")
			}
		})
	}
}

func TestChildDiscardOwnChildrenOnly(t *testing.T) {
	fx := newDelegFx(t)
	mine, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	theirs := fx.childOf(other)
	for _, token := range []string{fx.parentTok, fx.human("owner-1")} {
		for _, id := range []string{theirs, other, fx.parent, newUUID(), "bad-id"} {
			code, _, _ := fx.do("POST", childRoute(fx.parent, id, "/discard"), token, nil, nil)
			if code != 404 {
				t.Fatalf("foreign target=%d", code)
			}
		}
	}
	code, _, _ := fx.do("POST", childRoute(fx.parent, mine, "/discard"), fx.human("owner-2"), nil, nil)
	if code != 404 {
		t.Fatalf("non-owner=%d", code)
	}
	if fx.row(mine).EndedAt != nil {
		t.Fatal("unauthorized discard stopped child")
	}
}

func TestChildDiscardRefusesToHideChildWhenStopCannotBeDelivered(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	for len(fx.dc.send) < cap(fx.dc.send) {
		fx.dc.send <- []byte(`{}`)
	}
	code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if code != 503 {
		t.Fatalf("undeliveredstop=%d", code)
	}
	if fx.row(child).EndedAt != nil {
		t.Fatal("failed stop was recorded as ended")
	}
}

// A stop is asynchronous. Discard must wait for its authenticated final receipt
// instead of making the caller repeat a request that already reached the daemon.
func TestChildDiscardWaitsForWorkspaceFinalOnFirstAttempt(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: "owner-1", LaunchGeneration: "discard-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, child, m); err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	raw, _ := json.Marshal(binding)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_workspaces SET runtime_generation=1,runtime_binding=$2 WHERE session_id=$1`, child, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,'discard-token',$2,1,'connection')`, child, m.LaunchGeneration); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case raw := <-fx.dc.send:
				var kill protocol.KillSession
				if json.Unmarshal(raw, &kill) != nil || kill.Type != "kill_session" || kill.SessionID != child {
					continue
				}
				time.Sleep(40 * time.Millisecond)
				final := protocol.WorkspaceFinalization{Binding: binding, Message: protocol.SessionEnded{Type: "session_ended", SessionID: child}, Results: []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "wip/" + child}}}
				_, err := db.CommitLocalWorkspaceFinal(t.Context(), fx.pool, child, "discard-token", final)
				done <- err
				return
			case <-t.Context().Done():
				done <- t.Context().Err()
				return
			}
		}
	}()
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 200 {
		t.Fatalf("first discard=%d %+v; want 200 after final receipt", code, out)
	}
	if fx.row(child).ArchivedAt == nil {
		t.Fatal("settled child not archived")
	}
}
