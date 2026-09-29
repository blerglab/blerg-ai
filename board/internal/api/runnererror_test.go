package api_test

// A session that dies takes its reason with it: the runner's error broadcast
// is one-shot and long gone by the time blerg-board polls. The runner now
// persists it and reports it on the status endpoint, and blerg-board must put
// it on the card — otherwise a failed desktop session still reaches the user
// as a bare "error" (the trial's spawn black hole).

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// The reason is runner-supplied text of unbounded length (the preflight one
// already embeds a whole PATH). It lands in a card comment, so it gets capped
// rather than pasting an arbitrarily large blob into the card's history.
func TestIngestCapsAnEnormousErrorReason(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("huge reason"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", cookie, nil, map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d", resp.StatusCode)
	}

	fake.lifecycle, fake.errorReason = "error", strings.Repeat("x", 20_000)
	apiSrv.IngestOnce(ctx)

	events, err := db.ListCardEvents(ctx, pool, res.Card.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type != "comment" || !strings.Contains(string(ev.Data), "Session ended in error") {
			continue
		}
		found = true
		if len(ev.Data) > 2000 {
			t.Errorf("error comment is %d bytes; an unbounded runner string reached the card", len(ev.Data))
		}
		if !strings.Contains(string(ev.Data), "…") {
			t.Errorf("a truncated reason must say so; got %s", ev.Data)
		}
	}
	if !found {
		t.Fatalf("no error comment on the card; events = %+v", events)
	}
}

func TestIngestSurfacesErrorReasonOnCard(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", cookie, nil, map[string]string{"prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: %d", resp.StatusCode)
	}

	const reason = `repo "app" is not checked out under this daemon's repos root (/repos) — clone it there`
	fake.lifecycle, fake.errorReason = "error", reason
	apiSrv.IngestOnce(ctx)

	events, err := db.ListCardEvents(ctx, pool, res.Card.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "comment" && strings.Contains(string(ev.Data), "not checked out") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no card comment carried the runner's error reason; events = %+v", events)
	}

	// Idempotent: the session is already terminal, so a second pass must not
	// comment again (the ingest query skips settled sessions).
	apiSrv.IngestOnce(ctx)
	after, _ := db.ListCardEvents(ctx, pool, res.Card.ID, 0, 100)
	if len(after) != len(events) {
		t.Fatalf("second ingest added %d event(s); the reason must be written once", len(after)-len(events))
	}
}
