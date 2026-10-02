package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

const artSession = "0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55"

func artPath(id string) string { return "/sessions/" + artSession + "?artifact=" + id }

func artifactCard(t *testing.T, uiBase string) (srvToken string, cardID string, do func(method, path string, body any) *http.Response, pool *pgxpool.Pool) {
	t.Helper()
	srv, p := testServer(t)
	ctx := context.Background()
	srvAPI.SetRunner(api.RunnerConfig{PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: uiBase})
	f := false
	board, err := db.CreateBoard(ctx, p, db.BoardParams{Name: "work", RequireRepo: &f})
	if err != nil {
		t.Fatal(err)
	}
	pr := "the pr"
	res, err := db.CreateCard(ctx, p, board.ID, db.CardParams{
		Title: strPtr("write the spec"),
		Links: &[]db.Link{{Kind: "pr", URL: "https://github.com/o/r/pull/1", Label: &pr}},
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	tok := humanToken(t, srv)
	return tok, res.Card.ID, func(method, path string, body any) *http.Response {
		return request(t, srv, method, path, tok, nil, body)
	}, p
}

func cardLinks(t *testing.T, do func(method, path string, body any) *http.Response, id string) []db.Link {
	t.Helper()
	resp := do("GET", "/api/cards/"+id, nil)
	var c db.Card
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	return c.Links
}

func TestAddLinksAppendsAnArtifactAndResolvesTheRunnerAddress(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.test/sessions")
	lbl := "spec.pdf (v1)"
	resp := do("PATCH", "/api/cards/"+id, map[string]any{
		"add_links": []db.Link{{Kind: "artifact", URL: artPath("f1"), Label: &lbl}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add_links: %d", resp.StatusCode)
	}
	links := cardLinks(t, do, id)
	if len(links) != 2 || links[0].Kind != "pr" {
		t.Fatalf("the existing link must stay and the new one follow it: %+v", links)
	}
	want := "https://runner.test" + artPath("f1")
	if links[1].Kind != "artifact" || links[1].URL != want {
		t.Fatalf("a relative artifact link must be made absolute: %+v, want %s", links[1], want)
	}
	// adding the same file again changes nothing
	if resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: artPath("f1")}}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat add_links: %d", resp.StatusCode)
	}
	if n := len(cardLinks(t, do, id)); n != 2 {
		t.Fatalf("a link already on the card must not be added twice (%d links)", n)
	}
	// a different version of the file is another link
	do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: artPath("f2")}}})
	if n := len(cardLinks(t, do, id)); n != 3 {
		t.Fatalf("%d links, want 3", n)
	}
}

func TestAddLinksRejectsWhatIsNotTheRunnersViewer(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.test/sessions")
	cases := map[string]string{
		"another host":     "https://evil.example" + artPath("f1"),
		"script":           "javascript:alert(1)",
		"not a session id": "/sessions/nope?artifact=f1",
		"extra parameter":  artPath("f1") + "&x=1",
	}
	for name, u := range cases {
		resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: u}}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
	if n := len(cardLinks(t, do, id)); n != 1 {
		t.Fatalf("a rejected write must change nothing (%d links)", n)
	}
}

func TestArtifactLinkNeedsTheRunnerAddressForARelativePath(t *testing.T) {
	_, id, do, _ := artifactCard(t, "")
	resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: artPath("f1")}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: without RUNNER_UI_BASE a relative link cannot be resolved", resp.StatusCode)
	}
}

func TestConcurrentAddLinksLoseNothing(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.test/sessions")
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := do("PATCH", "/api/cards/"+id, map[string]any{
				"add_links": []db.Link{{Kind: "artifact", URL: artPath(fmt.Sprintf("file%d", i))}}})
			if resp.StatusCode != http.StatusOK {
				t.Errorf("add %d: %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	if got := len(cardLinks(t, do, id)); got != n+1 {
		t.Fatalf("%d links, want %d: appends running at once must all land", got, n+1)
	}
}

func TestMCPCardLinkAppendsAnArtifact(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	srvAPI.SetRunner(api.RunnerConfig{PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})
	f := false
	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{Name: "mcp", RequireRepo: &f})
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{Title: strPtr("card")}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "mcp agent",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "blerg_card_link",
		"arguments": map[string]any{
			"card_id": res.Card.ID, "kind": "artifact", "url": artPath("f9"), "label": "result.csv (v1)",
		},
	}}
	resp := request(t, srv, "POST", "/mcp", raw, nil, body)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if r, _ := out["result"].(map[string]any); r == nil || r["isError"] == true {
		t.Fatalf("blerg_card_link failed: %v", out)
	}
	card, _ := db.GetCard(ctx, pool, res.Card.ID)
	if len(card.Links) != 1 || card.Links[0].URL != "https://runner.test"+artPath("f9") {
		t.Fatalf("links: %+v", card.Links)
	}
}

func TestArtifactLinkWithoutTheRunnerAddressIsRefusedEvenWhenAbsolute(t *testing.T) {
	_, id, do, _ := artifactCard(t, "")
	resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: "https://evil.example/anything/" + artSession + "?artifact=a"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: with no known runner every artifact link is refused", resp.StatusCode)
	}
}

func TestArtifactLinkMustBeTheRunnersViewerExactly(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.test/sessions")
	for name, u := range map[string]string{
		"other scheme":    "http://runner.test" + artPath("f1"),
		"another path":    "https://runner.test/logout/" + artSession + "?artifact=f1",
		"double slash":    "https://runner.test//evil.example/" + artSession + "?artifact=f1",
		"scheme-relative": "//evil.example" + artPath("f1"),
		"dot segments":    "/sessions/../x/" + artSession + "?artifact=f1",
		"two params":      artPath("f1") + "&artifact=f2",
		"other port":      "https://runner.test:8443" + artPath("f1"),
	} {
		resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: u}}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
	if n := len(cardLinks(t, do, id)); n != 1 {
		t.Fatalf("nothing refused may be stored (%d links)", n)
	}
}

func TestLinksAndAddLinksInOneWriteAreRefused(t *testing.T) {
	_, id, do, _ := artifactCard(t, "https://runner.test/sessions")
	resp := do("PATCH", "/api/cards/"+id, map[string]any{
		"links":     []db.Link{{Kind: "url", URL: "https://example.com/x"}},
		"add_links": []db.Link{{Kind: "url", URL: "https://example.com/y"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestAddingALinkAlreadyThereChangesNothing(t *testing.T) {
	_, id, do, pool := artifactCard(t, "https://runner.test/sessions")
	do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: artPath("f1")}}})
	ctx := context.Background()
	before, err := db.GetCard(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM card_events WHERE card_id = $1`, id).Scan(&events)

	resp := do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "artifact", URL: artPath("f1")}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	after, _ := db.GetCard(ctx, pool, id)
	var eventsAfter int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM card_events WHERE card_id = $1`, id).Scan(&eventsAfter)
	if after.Version != before.Version || eventsAfter != events {
		t.Fatalf("a no-op append must not bump the version (%d -> %d) or add an event (%d -> %d)",
			before.Version, after.Version, events, eventsAfter)
	}
}

func TestLinksKeepTheirOrderAfterDuplicatesAndAppends(t *testing.T) {
	_, id, do, pool := artifactCard(t, "https://runner.test/sessions")
	ctx := context.Background()
	// a replace that repeats an entry burns a rank; later appends must still land after everything
	a, b := "https://example.com/a", "https://example.com/b"
	if _, err := db.UpdateCard(ctx, pool, id, db.CardParams{Links: &[]db.Link{
		{Kind: "url", URL: a}, {Kind: "url", URL: a}, {Kind: "url", URL: b}}}, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "url", URL: "https://example.com/c"}}})
	do("PATCH", "/api/cards/"+id, map[string]any{"add_links": []db.Link{{Kind: "url", URL: "https://example.com/d"}}})
	var got []string
	for _, l := range cardLinks(t, do, id) {
		got = append(got, l.URL[len("https://example.com/"):])
	}
	if fmt.Sprint(got) != "[a b c d]" {
		t.Fatalf("links = %v, want [a b c d] in the order added", got)
	}
}
