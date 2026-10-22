package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

const newBoardIntent = "33333333-3333-4333-8333-333333333333"

// newBoardToken is the credential core issues for one approved board (board-new exchange).
func newBoardToken(t *testing.T, intent, project string, caps []string) string {
	t.Helper()
	claims := identity.Claims{Sub: intent, Aud: "blerg-board", Kind: "agent", Project: project, OnBehalfOf: filingAccount, Lineage: filingAccount, Caps: caps, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(30 * time.Second).Unix()}
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	pb, _ := json.Marshal(claims)
	input := identity.EncodeSigningInput(hb, pb)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(input)))
}

func boardCap(t *testing.T, intent string, d boardfiling.BoardDraft) []string {
	t.Helper()
	c, err := boardfiling.BoardCapability(intent, boardfiling.BoardDigest(d))
	if err != nil {
		t.Fatal(err)
	}
	return []string{c}
}

func TestApprovedBoardCreatesExactlyTheApprovedDraftOnce(t *testing.T) {
	srv, pool := testServer(t)
	draft := boardfiling.BoardDraft{Name: "Launch", Description: "Ship it", Repos: []string{"team/app", "web"}, Columns: []string{"Todo", "Doing", "Done"}}
	tok := newBoardToken(t, newBoardIntent, "", boardCap(t, newBoardIntent, draft))
	var first struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Repos []string `json:"repos"`
	}
	resp := request(t, srv, "POST", "/api/boards/create-approved", tok, nil, draft)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create=%d", resp.StatusCode)
	}
	decodeBody(t, resp, &first)
	b, err := db.GetBoard(t.Context(), pool, first.ID)
	if err != nil || b.Name != "Launch" || len(b.Repos) != 2 || b.Repos[0] != "team/app" || b.Repos[1] != "web" || b.RequireRepo {
		t.Fatalf("board=%+v err=%v", b, err)
	}
	cols, err := db.ListColumns(t.Context(), pool, first.ID)
	if err != nil || len(cols) != 3 || cols[0].Name != "Todo" {
		t.Fatalf("columns=%v err=%v", cols, err)
	}
	// A retried approval answers the same board; it never creates a second one.
	resp = request(t, srv, "POST", "/api/boards/create-approved", tok, nil, draft)
	var again struct {
		ID string `json:"id"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay=%d", resp.StatusCode)
	}
	decodeBody(t, resp, &again)
	if again.ID != first.ID {
		t.Fatal("replay created another board")
	}
	boards, _ := db.ListBoards(t.Context(), pool)
	if len(boards) != 1 {
		t.Fatalf("boards=%d", len(boards))
	}
}

func TestApprovedBoardConcurrentUseCreatesOne(t *testing.T) {
	srv, pool := testServer(t)
	draft := boardfiling.BoardDraft{Name: "Race", Template: "focus-board"}
	tok := newBoardToken(t, newBoardIntent, "", boardCap(t, newBoardIntent, draft))
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			resp := request(t, srv, "POST", "/api/boards/create-approved", tok, nil, draft)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 && resp.StatusCode != 201 {
				t.Errorf("status=%d", resp.StatusCode)
			}
		})
	}
	wg.Wait()
	boards, _ := db.ListBoards(t.Context(), pool)
	if len(boards) != 1 {
		t.Fatalf("boards=%d", len(boards))
	}
	cols, _ := db.ListColumns(t.Context(), pool, boards[0].ID)
	if len(cols) < 3 {
		t.Fatal("template columns missing")
	}
}

func TestApprovedBoardRefusesEverythingButTheSignedDraft(t *testing.T) {
	srv, pool := testServer(t)
	draft := boardfiling.BoardDraft{Name: "Launch", Repos: []string{"team/app"}}
	good := boardCap(t, newBoardIntent, draft)
	card, _ := boardfiling.Capability(newBoardIntent, boardfiling.BoardDigest(draft))
	cases := []struct {
		name  string
		token string
		body  any
	}{
		{"changed draft", newBoardToken(t, newBoardIntent, "", good), boardfiling.BoardDraft{Name: "Launch", Repos: []string{"team/other"}}},
		{"extra field", newBoardToken(t, newBoardIntent, "", good), map[string]any{"name": "Launch", "repos": []string{"team/app"}, "run_as_me": true}},
		{"other subject", newBoardToken(t, "44444444-4444-4444-8444-444444444444", "", good), draft},
		{"board scoped", newBoardToken(t, newBoardIntent, "55555555-5555-4555-8555-555555555555", good), draft},
		{"card capability", newBoardToken(t, newBoardIntent, "", []string{card}), draft},
		{"extra capability", newBoardToken(t, newBoardIntent, "", append(good, "board.admin")), draft},
		{"admin person", adminToken(t), draft},
		{"service key", "svc-key-123", draft},
		{"invalid repo", newBoardToken(t, newBoardIntent, "", boardCap(t, newBoardIntent, boardfiling.BoardDraft{Name: "x", Repos: []string{"../x"}})), boardfiling.BoardDraft{Name: "x", Repos: []string{"../x"}}},
	}
	for _, c := range cases {
		resp := request(t, srv, "POST", "/api/boards/create-approved", c.token, nil, c.body)
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("%s: status=%d", c.name, resp.StatusCode)
		}
	}
	if boards, _ := db.ListBoards(t.Context(), pool); len(boards) != 0 {
		t.Fatal("a refused request created a board")
	}
	// A spent intent never creates a different board.
	resp := request(t, srv, "POST", "/api/boards/create-approved", newBoardToken(t, newBoardIntent, "", good), nil, draft)
	_ = resp.Body.Close()
	other := boardfiling.BoardDraft{Name: "Other"}
	resp = request(t, srv, "POST", "/api/boards/create-approved", newBoardToken(t, newBoardIntent, "", boardCap(t, newBoardIntent, other)), nil, other)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused intent=%d", resp.StatusCode)
	}
}

type repoChoiceRunner struct {
	*fakeRunner
	choices []runner.RepoChoice
}

func (r repoChoiceRunner) RepoChoices(context.Context) ([]runner.RepoChoice, error) {
	return r.choices, nil
}

func TestRepoChoicesForPeopleWhoCreateBoards(t *testing.T) {
	srv, _ := testServer(t)
	srvAPI.SetRunner(api.RunnerConfig{Driver: repoChoiceRunner{fakeRunner: &fakeRunner{}, choices: []runner.RepoChoice{
		{Repo: "team/app", Folder: "app", Desktops: []string{"Laptop"}}, {Repo: "bad repo", Folder: "bad repo"},
	}}})
	resp := request(t, srv, "GET", "/api/repo-choices", adminToken(t), nil, nil)
	var out struct {
		Available bool                `json:"available"`
		Repos     []runner.RepoChoice `json:"repos"`
	}
	decodeBody(t, resp, &out)
	if !out.Available || len(out.Repos) != 1 || out.Repos[0].Repo != "team/app" || out.Repos[0].Desktops[0] != "Laptop" {
		t.Fatalf("choices=%+v", out)
	}
	for _, tok := range []string{memberToken(t), testCoreToken(t, "agent", []string{"card.read", "board.admin"})} {
		resp := request(t, srv, "GET", "/api/repo-choices", tok, nil, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin or agent=%d", resp.StatusCode)
		}
	}
}
