package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBlackboardRequestSetupRelaysToMain covers "Set it up with Main" (BLG-450 phase 2, part A):
// it reuses the existing worker-notice relay mechanism (sendRelay) to deliver one fixed,
// server-authored message to the workspace's own root session, never text the caller supplies.
func TestBlackboardRequestSetupRelaysToMain(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetInputReceipts(true) // the deterministic client id (L5) requires receipt support
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)

	request := func(method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	rec := request("POST", "/api/sessions/"+fx.parent+"/plan/request-setup", "")
	if rec.Code != 401 {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("POST", "/api/sessions/"+fx.parent+"/plan/request-setup", fx.parentTok)
	if rec.Code == 200 {
		t.Fatalf("a session's own agent token should not be able to ask for setup: %d %s", rec.Code, rec.Body.String())
	}

	// Root resolution (own test root, never asked yet): a child session's own ancestry resolves
	// to its root — the request goes to Main even when asked from a worker's view of the
	// blackboard tab. Checked on a root that has had no prior request-setup call, so the dedupe
	// added below (L5) cannot be the reason a relay does or doesn't show up here.
	child, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Worker"})
	_ = childTok
	rec = request("POST", "/api/sessions/"+child+"/plan/request-setup", fx.human("owner-1"))
	if rec.Code != 200 {
		t.Fatalf("request setup from a child: %d %s", rec.Code, rec.Body.String())
	}
	received := fx.parentNote(fx.parent, time.Second)
	if received.Source != blackboardSetupSource || received.Text == "" {
		t.Fatalf("expected the child's request to reach the root: %+v", received)
	}

	// Dedupe (BLG-450 review L5): two rapid "Set it up with Main" clicks must deliver exactly one
	// relay turn, not two. The child's call above already put one pending relay on fx.parent's
	// wire; a second request-setup call for the same workspace — from the root itself this time,
	// proving the dedupe key is the resolved root and not the literal calling session — must not
	// put a second agent_user_message there.
	rec = request("POST", "/api/sessions/"+fx.parent+"/plan/request-setup", fx.human("owner-1"))
	if rec.Code != 200 {
		t.Fatalf("second (duplicate) request-setup: %d %s", rec.Code, rec.Body.String())
	}
	second := fx.parentNote(fx.parent, 300*time.Millisecond)
	if second.Source == blackboardSetupSource {
		t.Fatalf("a second rapid request-setup call delivered a second relay turn, want exactly one: %+v", second)
	}

	// A fresh workspace (its own dedupe slot) still gets its own, independent relay — the dedupe
	// is per workspace, not a global one-ever-ism.
	root2, root2Tok := fx.session(sessOpts{owner: "owner-1", title: "Main 2"})
	_ = root2Tok
	fx.hub.SetSessionOwner(root2, fx.dc.ID)
	rec = request("POST", "/api/sessions/"+root2+"/plan/request-setup", fx.human("owner-1"))
	if rec.Code != 200 {
		t.Fatalf("request setup on a second, unrelated workspace: %d %s", rec.Code, rec.Body.String())
	}
	third := fx.parentNote(root2, time.Second)
	if third.Source != blackboardSetupSource {
		t.Fatalf("a different workspace's first request must still be delivered: %+v", third)
	}

	// Another account's session is invisible, not merely refused differently.
	other, _ := fx.session(sessOpts{owner: "owner-2", title: "Someone else"})
	rec = request("POST", "/api/sessions/"+other+"/plan/request-setup", fx.human("owner-1"))
	if rec.Code != 404 {
		t.Fatalf("other account's session: %d %s", rec.Code, rec.Body.String())
	}
}
