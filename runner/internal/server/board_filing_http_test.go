package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const filingHTTPBoard = "33333333-3333-4333-8333-333333333333"
const filingHTTPCard = "44444444-4444-4444-8444-444444444444"

var filingHTTPProof = mcpgw.Proof{AccountID: "11111111-1111-4111-8111-111111111111", SessionID: "22222222-2222-4222-8222-222222222222"}

func filingReadFixtureToken() string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"55555555-5555-4555-8555-555555555555"}`)) + ".c2ln"
}

func TestBoardFilingHTTPReadAlwaysRevokesAndDoesNotFollowRedirects(t *testing.T) {
	for _, mode := range []string{"ok", "denied", "redirect", "oversized", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var revoked, leaked atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer target.Close()
			board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/boards/"+filingHTTPBoard || r.Header.Get("Authorization") != "Bearer "+filingReadFixtureToken() {
					t.Error("read changed trusted Board route or grant")
				}
				switch mode {
				case "ok":
					_, _ = w.Write([]byte(`{"id":"` + filingHTTPBoard + `","repos":["target-repo"],"require_repo":true,"include_component_version":true}`))
				case "denied":
					http.Error(w, "private fixture server detail", http.StatusForbidden)
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20) + `{}`))
				case "malformed":
					_, _ = w.Write([]byte(`{`))
				}
			}))
			defer board.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Internal-Key") != "fixture-internal" {
					t.Error("Core authentication missing")
				}
				switch r.URL.Path {
				case "/internal/tokens/exchange":
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if got["account_id"] != filingHTTPProof.AccountID || got["session_id"] != filingHTTPProof.SessionID || got["board_id"] != filingHTTPBoard || got["read_only"] != true {
						t.Error("read substituted proof or board")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"token": filingReadFixtureToken(), "expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
				case "/internal/tokens/exchange/revoke":
					var got map[string]string
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if got["account_id"] != filingHTTPProof.AccountID || got["token_sub"] != "55555555-5555-4555-8555-555555555555" {
						t.Error("revoke substituted account or grant")
					}
					revoked.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Error("unexpected Core endpoint")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer core.Close()
			up := &httpBoardFiling{base: board.URL, core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-internal", HTTP: core.Client()}, version: "test-version", commit: "abcdef1"}
			got, err := up.Prepare(t.Context(), filingHTTPProof, filingHTTPBoard, boardfiling.Draft{Title: "Work"})
			if mode == "ok" {
				if err != nil || len(got.Repos) != 1 || got.Repos[0] != "target-repo" || got.Origin.ComponentVersion != "test-version" {
					t.Fatal("prepare did not freeze target defaults before approval")
				}
			} else if err == nil {
				t.Fatal("untrusted response accepted")
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("upstream error body leaked")
			}
			if revoked.Load() != 1 || leaked.Load() != 0 {
				t.Fatal("read credential not revoked or followed redirect")
			}
		})
	}
}

func TestBoardFilingHTTPCreateBindsApprovedDigestAndOnlyReturnsReceipt(t *testing.T) {
	draft := boardfiling.Draft{Title: "Approved", Body: "fixture-private-reproduction", Sensitive: true, Repos: []string{"target"}, Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Repo: "origin", Engine: "codex"}}
	intent := "66666666-6666-4666-8666-666666666666"
	var writes atomic.Int32
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/boards/"+filingHTTPBoard+"/file-card" || r.Header.Get("Authorization") != "Bearer fixture-create-grant" {
			t.Error("create changed trusted route or credential")
		}
		var got boardfiling.Draft
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if boardfiling.Digest(filingHTTPBoard, got) != boardfiling.Digest(filingHTTPBoard, draft) {
			t.Error("create mutated approved draft")
		}
		writes.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + filingHTTPCard + `","board_id":"` + filingHTTPBoard + `","number":7,"body":"fixture-private-reproduction","token":"fixture-private-upstream"}`))
	}))
	defer board.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/tokens/exchange/board-create" || r.Header.Get("X-Internal-Key") != "fixture-internal" {
			t.Error("create used wrong Core capability endpoint")
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got["account_id"] != filingHTTPProof.AccountID || got["session_id"] != filingHTTPProof.SessionID || got["intent"] != intent || got["board_id"] != filingHTTPBoard || got["digest"] != boardfiling.Digest(filingHTTPBoard, draft) {
			t.Error("approval capability lost account, intent or payload binding")
		}
		_, _ = w.Write([]byte(`{"token":"fixture-create-grant"}`))
	}))
	defer core.Close()
	up := &httpBoardFiling{base: board.URL, core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-internal", HTTP: core.Client()}}
	raw, err := up.Create(t.Context(), filingHTTPProof, intent, filingHTTPBoard, draft)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["id"] != filingHTTPCard || fields["board_id"] != filingHTTPBoard || fields["number"] != float64(7) || strings.Contains(string(raw), "private") {
		t.Fatal("unexpected upstream metadata leaked instead of a receipt")
	}
	if writes.Load() != 1 {
		t.Fatal("approved write count changed")
	}
}

func TestBoardFilingHTTPCreateRejectsCoreRedirectOversizeAndMalformedReceipt(t *testing.T) {
	for _, mode := range []string{"core-redirect", "core-oversize", "board-redirect", "wrong-receipt", "malformed-receipt", "board-denied"} {
		t.Run(mode, func(t *testing.T) {
			var leaked, writes atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer target.Close()
			board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				switch mode {
				case "board-redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "wrong-receipt":
					_, _ = w.Write([]byte(`{"id":"` + filingHTTPCard + `","board_id":"other","number":1}`))
				case "board-denied":
					http.Error(w, "private fixture body", http.StatusUnprocessableEntity)
				default:
					_, _ = w.Write([]byte(`[]`))
				}
			}))
			defer board.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "core-redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write([]byte(`{"token":"fixture-create-grant"}`))
				if mode == "core-oversize" {
					_, _ = w.Write([]byte(strings.Repeat(" ", 16<<10)))
				}
			}))
			defer core.Close()
			up := &httpBoardFiling{base: board.URL, core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-internal", HTTP: core.Client()}}
			_, err := up.Create(context.Background(), filingHTTPProof, "66666666-6666-4666-8666-666666666666", filingHTTPBoard, boardfiling.Draft{Title: "Approved"})
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid upstream response accepted or leaked")
			}
			if leaked.Load() != 0 {
				t.Fatal("credential followed cross-host redirect")
			}
			if strings.HasPrefix(mode, "core-") && writes.Load() != 0 {
				t.Fatal("invalid Core credential response caused Board write")
			}
			if mode == "wrong-receipt" || mode == "malformed-receipt" || mode == "board-redirect" {
				if !errors.Is(err, mcpgw.ErrOutcomeUnknown) {
					t.Fatal("possible Board side effect was treated as definite failure")
				}
			}
		})
	}
}
