package mcpgw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStandingExternalAndBoardWire(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(map[bool]string{false: "token", true: "human"}[human], func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				standing, _ := body["standing"].(bool)
				if standing != human {
					t.Errorf("%s standing=%v want %v", r.URL.Path, standing, human)
				}
				if r.Header.Get("X-Internal-Key") != "fixture-key" {
					t.Error("missing internal key")
				}
				http.Error(w, "not found", http.StatusNotFound)
			}))
			defer srv.Close()
			proof := Proof{AccountID: "owner", TokenID: "token"}
			if human {
				proof.TokenID = ""
				proof.SessionID = "original-login"
			}
			core := HTTPCoreClient{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
			_, _ = core.Token(context.Background(), proof, "connection")
			board := HTTPBoardExchanger{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
			_, _ = board.Exchange(context.Background(), proof, "board", "session", true)
			_, _ = board.ExchangeReadOnly(context.Background(), proof, "board", "session", true)
			if calls != 3 {
				t.Errorf("calls=%d want 3", calls)
			}
		})
	}
}

func TestStandingBoardExchangeIsSelectedByTheRealCaller(t *testing.T) {
	for _, tc := range []struct {
		name, tool              string
		readOnly, cut, standing bool
	}{
		{"standing read", "blerg_card_get", true, false, true},
		{"standing write", "blerg_card_create", false, false, true},
		{"one-shot cut", "blerg_release_close", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				got, _ := body["standing"].(bool)
				if got != tc.standing {
					t.Errorf("actual %s caller sent standing=%v want %v", tc.name, got, tc.standing)
				}
				if body["session_id"] != "original-login" {
					t.Errorf("proof rewritten: %+v", body)
				}
				http.Error(w, "not found", http.StatusNotFound)
			}))
			defer core.Close()
			observations := 0
			b := newBoardHarness(t, func(c *Config) {
				c.ObserveAuthority = func(context.Context, AuthorityObservation) { observations++ }
				c.Board = &HTTPBoardExchanger{BaseURL: core.URL, InternalKey: "fixture-key", HTTP: core.Client()}
			})
			_, _ = b.gw.CallBoard(t.Context(), BoardCall{Proof: Proof{AccountID: "owner", SessionID: "original-login"}, BoardID: boardTestID, SessionRef: "session", Tool: tc.tool, Arguments: []byte(`{}`), ReadOnly: tc.readOnly, CutExecutor: tc.cut})
			if !tc.standing && observations != 0 {
				t.Fatal("exact one-shot cut emitted a standing permission observation")
			}
			if calls != 1 {
				t.Fatalf("exchange calls=%d want 1", calls)
			}
		})
	}
}

func TestStandingCoreRestartRemainsUnavailable(t *testing.T) {
	for _, status := range []int{503, 404} {
		for _, endpoint := range []string{"board", "mcp"} {
			t.Run(fmt.Sprintf("%s/%d", endpoint, status), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "not found", status) }))
				defer srv.Close()
				proof := Proof{AccountID: "owner", SessionID: "original-login"}
				var err error
				if endpoint == "board" {
					c := HTTPBoardExchanger{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
					_, err = c.Exchange(t.Context(), proof, "board", "session", true)
				} else {
					c := HTTPCoreClient{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
					_, err = c.Token(t.Context(), proof, "connection")
				}
				if got := AuthorityState(err); got != "unavailable" {
					t.Fatalf("restart %d classified %s, want unavailable", status, got)
				}
			})
		}
	}
}

func TestStandingRefusalRequiresExplicitAuthorityAndTransportFailsClosed(t *testing.T) {
	for _, state := range []string{"", "unavailable", "future", "suspended", "revoked"} {
		t.Run(state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Blerg-Standing-State", state)
				http.Error(w, "Not Found", http.StatusNotFound)
			}))
			defer srv.Close()
			proof := Proof{AccountID: "owner", SessionID: "original-chain"}
			core := HTTPCoreClient{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
			board := HTTPBoardExchanger{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
			_, mcpErr := core.Token(t.Context(), proof, "connection")
			_, boardErr := board.Exchange(t.Context(), proof, "board", "session", true)
			want := "unavailable"
			if state == "suspended" || state == "revoked" {
				want = state
			}
			for _, err := range []error{mcpErr, boardErr} {
				if got := AuthorityState(err); got != want {
					t.Errorf("explicit state %q classified %s, want %s", state, got, want)
				}
			}
		})
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		cl := &http.Client{Transport: reviewAuthorityTransport(func(*http.Request) (*http.Response, error) { return nil, failure })}
		core := HTTPCoreClient{BaseURL: "http://core.example", InternalKey: "fixture-key", HTTP: cl}
		board := HTTPBoardExchanger{BaseURL: "http://core.example", InternalKey: "fixture-key", HTTP: cl}
		proof := Proof{AccountID: "owner", SessionID: "chain"}
		_, err := core.Token(t.Context(), proof, "connection")
		if AuthorityState(err) != "unavailable" {
			t.Fatal("transport failure became authority loss")
		}
		_, err = board.Exchange(t.Context(), proof, "board", "session", true)
		if AuthorityState(err) != "unavailable" {
			t.Fatal("exchange transport failure became authority loss")
		}
	}
}

func TestStandingBoardOwnerMissingExplainsRepair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Blerg-Standing-State", "unavailable")
		w.Header().Set("X-Blerg-Standing-Cause", "board_owner_missing")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := HTTPBoardExchanger{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
	_, err := c.Exchange(t.Context(), Proof{AccountID: "owner", SessionID: "chain"}, "board", "session", true)
	if AuthorityState(err) != "unavailable" || err == nil || !strings.Contains(err.Error(), "no recorded owner") {
		t.Fatalf("ownerless board lost actionable cause: %v", err)
	}
}
