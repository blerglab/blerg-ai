package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
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
