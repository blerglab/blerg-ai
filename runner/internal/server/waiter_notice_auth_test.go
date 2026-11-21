package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDesktopWaiterNoticeUsesOnlyItsOwnHosting(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	id := postAsk(t, f, token, session, "Which branch?", 1, "")
	if _, err := db.AnswerMessage(t.Context(), f.pool, id, "Use the release branch."); err != nil {
		t.Fatal(err)
	}
	binding := *f.dc.DesktopBinding
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		b := binding
		switch {
		case strings.Contains(raw["token"], "foreign"):
			b.DesktopID = "00000000-0000-4000-8000-0000000000d9"
		case strings.Contains(raw["token"], "stale"):
			b.Revision++
		}
		_ = json.NewEncoder(w).Encode(desktopauth.Binding{DesktopID: b.DesktopID, OwnerID: b.OwnerID, Revision: b.Revision, TokenID: b.TokenID, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer core.Close()
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-internal-key"
	for _, credential := range []string{"bdt_fixture-foreign", "bdt_fixture-stale"} {
		r := privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, credential, `{"waiter_ended":true}`)
		if r.Code != 403 {
			t.Fatalf("foreign hosting expiry: %d %s", r.Code, r.Body.String())
		}
		if m := mustChildMessage(t, f, id); m.NoWait || m.WaiterEnded {
			t.Fatalf("foreign hosting ended waiter: %+v", m)
		}
	}
	r := privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, "bdt_fixture-mine", `{"waiter_ended":true}`)
	if r.Code != 200 {
		t.Fatalf("own hosting expiry: %d %s", r.Code, r.Body.String())
	}
	if m := mustChildMessage(t, f, id); !m.NoWait || !m.WaiterEnded {
		t.Fatalf("host did not preserve its late answer: %+v", m)
	}
}

func TestPersonOnlyWaiterCloseAcknowledgesItsPersistedNotice(t *testing.T) {
	f := newDelegFx(t)
	id := postAsk(t, f, delegDaemonToken, f.parent, "May the command run?", 1, `,"owner_only":true`)
	r := privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, delegDaemonToken, `{"waiter_ended":true}`)
	var receipt struct {
		WaiterEnded bool `json:"waiter_ended"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &receipt)
	if r.Code != 403 || !receipt.WaiterEnded {
		t.Fatalf("person-only close did not acknowledge persistence: %d %s", r.Code, r.Body.String())
	}
	if m := mustChildMessage(t, f, id); m.Status != "open" || !m.NoWait || !m.WaiterEnded {
		t.Fatalf("acknowledgment lost the person's question: %+v", m)
	}
}
