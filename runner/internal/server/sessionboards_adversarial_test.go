package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type boardAuditTransport func(*http.Request) (*http.Response, error)

func (f boardAuditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionBoardTransportHoldsOwnerProofAndNeverReturnsCredentials(t *testing.T) {
	proof := mcpgw.Proof{AccountID: "fixture-account", SessionID: "fixture-session"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/boards/list" || r.Method != http.MethodPost || r.Header.Get("X-Internal-Key") != "fixture-internal" {
			t.Error("discovery changed internal route or authentication")
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if len(got) != 2 || got["account_id"] != proof.AccountID || got["session_id"] != proof.SessionID {
			t.Error("discovery widened or substituted owner proof")
		}
		_, _ = w.Write([]byte(`{"boards":[{"id":"fixture-board","name":"Work","token":"fixture-private-upstream"}],"token":"fixture-private-upstream"}`))
	}))
	defer srv.Close()
	c := &HTTPMCPCoreClient{BaseURL: srv.URL, InternalKey: "fixture-internal", HTTP: srv.Client()}
	got, err := c.ListSessionBoards(t.Context(), proof)
	if err != nil || len(got) != 1 || got[0].ID != "fixture-board" {
		t.Fatal("discovery did not return the permitted board")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatal("Core credential field leaked into browser board metadata")
	}
}

func TestSessionBoardTransportRefusesInvalidProofWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	c := &HTTPMCPCoreClient{BaseURL: "https://core.example.test", InternalKey: "fixture", HTTP: &http.Client{Transport: boardAuditTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	})}}
	for _, p := range []mcpgw.Proof{{AccountID: "a"}, {AccountID: "a", SessionID: "s", TokenID: "t"}, {SessionID: "s"}} {
		if _, err := c.ListSessionBoards(t.Context(), p); err == nil {
			t.Error("invalid proof accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid proof contacted Core")
	}
}

func TestSessionBoardTransportDoesNotFollowCredentialRedirect(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c := &HTTPMCPCoreClient{BaseURL: redirect.URL, InternalKey: "fixture", HTTP: redirect.Client()}
	if _, err := c.ListSessionBoards(t.Context(), mcpgw.Proof{AccountID: "a", SessionID: "s"}); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("internal credential followed redirect")
	}
}

func TestSessionBoardTransportCapsTimeWithoutDependingOnConfiguredClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &HTTPMCPCoreClient{BaseURL: "https://core.example.test", InternalKey: "fixture", HTTP: &http.Client{Transport: boardAuditTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}}
		start := time.Now()
		if _, err := c.ListSessionBoards(context.Background(), mcpgw.Proof{AccountID: "a", SessionID: "s"}); err == nil {
			t.Fatal("stalled Core accepted")
		}
		if elapsed := time.Since(start); elapsed != boardDiscoveryTimeout {
			t.Fatalf("discovery ran for %s, want %s", elapsed, boardDiscoveryTimeout)
		}
	})
}

func TestSessionBoardTransportRejectsMalformedProtocol(t *testing.T) {
	for name, body := range map[string]string{
		"too-large": strings.Repeat(" ", maxSessionBoardsBytes) + `{"boards":[]}`,
		"missing":   `{}`, "null": `{"boards":null}`, "wrong-type": `{"boards":{}}`,
		"duplicate": `{"boards":[{"id":"b1","name":"Work"},{"id":"b1","name":"Work"}]}`,
		"path":      `{"boards":[{"id":"../b1","name":"Work"}]}`,
		"newline":   `{"boards":[{"id":"b1","name":"Work\nsecret"}]}`,
		"trailing":  `{"boards":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			c := &HTTPMCPCoreClient{BaseURL: srv.URL, InternalKey: "fixture", HTTP: srv.Client()}
			_, err := c.ListSessionBoards(t.Context(), mcpgw.Proof{AccountID: "a", SessionID: "s"})
			if err == nil {
				t.Fatal("malformed Core response accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream response leaked through error")
			}
		})
	}
}

func TestSessionBoardTransportDistinguishesOldCoreFromDeadProof(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{true: "old-core", false: "dead-proof"}[old], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if old {
					http.NotFound(w, r)
				} else {
					http.Error(w, "not found", 404)
				}
			}))
			defer srv.Close()
			c := &HTTPMCPCoreClient{BaseURL: srv.URL, InternalKey: "fixture", HTTP: srv.Client()}
			_, err := c.ListSessionBoards(t.Context(), mcpgw.Proof{AccountID: "a", SessionID: "s"})
			want := ErrMCPProofInvalid
			if old {
				want = ErrMCPCoreOutdated
			}
			if !errors.Is(err, want) {
				t.Fatalf("wrong discovery error: %v, want %v", err, want)
			}
		})
	}
}

func TestSessionBoardScopedHumanCannotLaunchOutsideBoard(t *testing.T) {
	fx := newMCPFx(t)
	fx.hub.SetJobManager(nil)
	list := enableLaunchBoard(fx)
	bearer := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: mcpAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "other-board", Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	rec := fx.do(fx.api.HandlePostSessions, bearer, map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "board_id": "board-7"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("scoped human selected different board: %d", rec.Code)
	}
	if fx.count("sessions") != 0 || len(fx.dc.send) != 0 || len(list.proofs) != 0 {
		t.Fatal("scope refusal performed discovery, recorded session, or spawned work")
	}
}

func TestSessionBoardScopedHumanDiscoveryFiltersToCredentialBoard(t *testing.T) {
	fx := newMCPFx(t)
	list := enableLaunchBoard(fx)
	list.boards = []SessionBoard{{ID: "board-7", Name: "Allowed"}, {ID: "other-board", Name: "Private other board"}}
	bearer := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: mcpAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "board-7", Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/session-boards", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	fx.api.HandleSessionBoards(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped discovery status %d", rec.Code)
	}
	var out struct {
		Configured bool           `json:"configured"`
		Boards     []SessionBoard `json:"boards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Configured || len(out.Boards) != 1 || out.Boards[0].ID != "board-7" || strings.Contains(rec.Body.String(), "Private") {
		t.Fatal("scoped credential enumerated another board")
	}
	if len(list.proofs) != 1 || list.proofs[0] != (mcpgw.Proof{AccountID: mcpAcct, SessionID: testSID}) {
		t.Fatal("scoped discovery substituted account/session proof")
	}
}
