package server

// A session that is private from the start (a grant or a cron) is written
// private, with its owner, in the SAME insert as its row: at no point, in
// particular between the insert and MarkPrivate, can another account list it.
// The seam (API.beforeMarkPrivate) runs exactly in that window.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const foreignAcct = "user-2"

// peekAsForeign lists sessions as another account, over every surface that
// lists them, and reports the first leak it finds ("" = none).
func peekAsForeign(fx *mcpFx, sessionID string) string {
	ctx := context.Background()
	tok := mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: foreignAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{coreAuthBrowserCap},
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	rec := privDo(fx.api.HandleGetSessions, http.MethodGet, "/api/sessions", "", tok, "")
	if rec.Code != http.StatusOK {
		return "GET /api/sessions failed: " + rec.Body.String()
	}
	if strings.Contains(rec.Body.String(), sessionID) {
		return "GET /api/sessions lists the session"
	}
	bc := &BrowserConn{ID: "peek", AccountID: foreignAcct, send: make(chan []byte, 4)}
	sendInitialState(ctx, bc, fx.hub, fx.pool)
	for len(bc.send) > 0 {
		if strings.Contains(string(<-bc.send), sessionID) {
			return "the websocket initial_state lists the session"
		}
	}
	row, err := db.GetSession(ctx, fx.pool, sessionID)
	if err != nil || row == nil {
		return "the row is not there yet"
	}
	if !row.Private || row.SpawningAccountID == nil || *row.SpawningAccountID != mcpAcct {
		return "the row is not private to its owner at insert time"
	}
	return ""
}

// installPeek makes every marking peek first; leaks and the number of peeks are recorded.
func installPeek(fx *mcpFx) (leaks *[]string, peeks *int) {
	var l []string
	n := 0
	fx.api.beforeMarkPrivate = func(id string) {
		n++
		if msg := peekAsForeign(fx, id); msg != "" {
			l = append(l, msg)
		}
	}
	return &l, &n
}

func TestGrantSessionIsNeverVisibleToAnotherAccountBeforeMarking(t *testing.T) {
	for _, site := range spawnSites() {
		t.Run(site.name, func(t *testing.T) {
			fx := newMCPFx(t)
			leaks, peeks := installPeek(fx)
			out := site.start(fx, fx.mustGrant(site.target(fx)), "")
			if !out.accepted() {
				t.Fatalf("refused: %d %s", out.status, out.msg)
			}
			if *peeks == 0 {
				t.Fatal("the marking seam never ran: the test proves nothing")
			}
			for _, l := range *leaks {
				t.Error(l)
			}
			// The hub already withheld the session's broadcasts.
			if fx.hub.accountCanSee(foreignAcct, out.sessionID) || !fx.hub.accountCanSee(mcpAcct, out.sessionID) {
				t.Error("the hub's record disagrees with the row")
			}
		})
	}
}

func TestCronSessionIsNeverVisibleToAnotherAccountBeforeMarking(t *testing.T) {
	for _, runtime := range []string{"docker", "auto"} {
		t.Run(runtime, func(t *testing.T) {
			cf := newCronFx(t)
			leaks, peeks := installPeek(cf.mcpFx)
			c := cf.newCron(func(c *db.Cron) { c.Runtime = runtime })
			sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if *peeks == 0 {
				t.Fatal("the marking seam never ran: the test proves nothing")
			}
			for _, l := range *leaks {
				t.Error(l)
			}
			row := cf.session(sid)
			if !row.Private || row.CronID == nil || *row.CronID != c.ID {
				t.Errorf("row: private=%v cron=%v", row.Private, row.CronID)
			}
		})
	}
	// A cron with no connections has no grant to mark the session, only the cron itself.
	t.Run("no connections", func(t *testing.T) {
		cf := newCronFx(t)
		leaks, peeks := installPeek(cf.mcpFx)
		c := cf.newCron(func(c *db.Cron) { c.MCP = json.RawMessage("[]"); c.Runtime = "docker" })
		if _, err := cf.svc.Start(context.Background(), c, cf.run(c)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if *peeks == 0 {
			t.Fatal("the marking seam never ran")
		}
		for _, l := range *leaks {
			t.Error(l)
		}
	})
}
