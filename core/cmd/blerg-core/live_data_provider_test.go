package main

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func TestLiveDataProviderRequiresExactCommittedReceipt(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	owner := "00000000-0000-4000-8000-000000000731"
	connection := "00000000-0000-4000-8000-000000000732"
	if _, err := pool.Exec(t.Context(), `INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES($1,'local','provider-fixture','provider@example.test','member')`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO mcp_connections(id,account_id,name,url,auth_kind) VALUES($1,$2,'market','https://market.example/mcp','none')`, connection, owner); err != nil {
		t.Fatal(err)
	}
	receipt := ld.AttemptReceipt{Scope: ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: owner, Connection: connection, Binding: "00000000-0000-4000-8000-000000000004", Tool: "quotes", Hash: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), Revision: 1, OwnerEpoch: 1, AuthorityEpoch: 1, ConnectionGeneration: 1}, Attempt: "00000000-0000-4000-8000-000000000111", Stage: "list"}
	cases := []string{"unavailable", "refused", "wrong attempt", "wrong stage", "wrong owner", "non-success receipt", "oversize receipt", "short receipt read", "redirect receipt", "accepted"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			var redirected atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				_ = json.NewEncoder(w).Encode(receipt)
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/live-data/attempt" || r.Header.Get("X-Internal-Key") != "fixture-internal" {
					t.Error("wrong callback transport")
					w.WriteHeader(403)
					return
				}
				if name == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if name == "refused" {
					w.WriteHeader(403)
					return
				}
				if name == "non-success receipt" {
					w.WriteHeader(403)
				}
				if name == "redirect receipt" {
					w.Header().Set("Location", target.URL)
					w.WriteHeader(302)
				}
				if name == "oversize receipt" || name == "short receipt read" {
					raw, _ := json.Marshal(receipt)
					if name == "short receipt read" {
						w.Header().Set("Content-Length", strconv.Itoa(len(raw)+100))
					}
					_, _ = w.Write(raw)
					if name == "oversize receipt" {
						_, _ = w.Write([]byte(strings.Repeat(" ", 8193)))
					}
					return
				}
				got := receipt
				switch name {
				case "wrong attempt":
					got.Attempt = "other"
				case "wrong stage":
					got.Stage = "call"
				case "wrong owner":
					got.Scope.Owner = "other"
				}
				_ = json.NewEncoder(w).Encode(got)
			}))
			defer server.Close()
			resolver := liveDataProvider(api.Deps{InternalKey: "fixture-internal", LiveDataRunnerURL: server.URL, LiveDataRunnerClient: server.Client(), MCPConnections: mcpconn.NewService(db.NewPgStore(pool), nil, netguard.Policy{})})
			var before, after int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM mcp_secret_access_log`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			credential, err := resolver(t.Context(), receipt)
			if name == "accepted" {
				if err != nil || credential.URL != "https://market.example/mcp" {
					t.Fatal("exact receipt refused", err)
				}
			} else if err == nil || credential.URL != "" {
				t.Fatal("credential released without exact receipt")
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM mcp_secret_access_log`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if name == "unavailable" && !errors.Is(err, livedata.ErrUnavailable) {
				t.Fatal("callback outage became revocation", err)
			}
			want := before
			if name == "accepted" {
				want++
			}
			if name == "accepted" {
				var attempt string
				if err := pool.QueryRow(t.Context(), `SELECT fetched_by_live_attempt_id::text FROM mcp_secret_access_log ORDER BY id DESC LIMIT 1`).Scan(&attempt); err != nil || attempt != receipt.Attempt {
					t.Fatal("credential access lost attempt attribution", err)
				}
			}
			if redirected.Load() != 0 {
				t.Fatal("credential callback followed redirect")
			}
			if after != want {
				t.Fatal("secret accessed before receipt confirmation")
			}
		})
	}
}
