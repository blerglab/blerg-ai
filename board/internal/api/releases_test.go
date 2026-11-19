package api_test

import (
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestReleaseRoutes(t *testing.T) {
	srv, pool := testServer(t)
	b, e := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Releases"})
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/boards/" + b.ID + "/releases"
	resp := request(t, srv, "POST", path, adminToken(t), nil, map[string]any{"name": "First", "status": "planned"})
	if resp.StatusCode != 201 {
		t.Fatalf("create status=%d, want 201", resp.StatusCode)
	}
	var r db.Release
	decodeBody(t, resp, &r)
	resp = request(t, srv, "GET", path, adminToken(t), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	var list []db.Release
	decodeBody(t, resp, &list)
	if len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("list=%+v", list)
	}
	resp = request(t, srv, "POST", path+"/"+r.ID+"/close", adminToken(t), nil, map[string]any{"next_release_id": ""})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(t, srv, "GET", path+"/"+r.ID, adminToken(t), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	var view db.ReleaseView
	decodeBody(t, resp, &view)
	if view.Release.Status != "released" {
		t.Fatal(view.Release)
	}
}

func TestReleasePersonBehindAndScope(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	b, e := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Release permissions"})
	if e != nil {
		t.Fatal(e)
	}
	other, e := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Other board"})
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/boards/" + b.ID + "/releases"
	for _, person := range []bool{false, true} {
		token := projectTokenProof(t, "agent", b.ID, boardAgentCaps, person)
		resp := request(t, srv, "POST", path, token, nil, map[string]any{"name": "First", "status": "planned"})
		want := 403
		if person {
			want = 201
		}
		if resp.StatusCode != want {
			t.Fatalf("person=%v status=%d want=%d", person, resp.StatusCode, want)
		}
		resp.Body.Close()
		resp = request(t, srv, "GET", "/api/boards/"+other.ID+"/releases", token, nil, nil)
		if resp.StatusCode != 404 {
			t.Fatalf("foreign board=%d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, person := range []bool{false, true} {
		token := projectTokenProof(t, "agent", b.ID, boardAgentCaps, person)
		resp := request(t, srv, "POST", "/mcp", token, nil, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_release_save", "arguments": map[string]any{"board_id": b.ID, "name": "MCP release", "status": "planned"}}})
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		decodeBody(t, resp, &out)
		if out.Result.IsError == person {
			t.Fatalf("MCP person=%v error=%v", person, out.Result.IsError)
		}
	}
}

func TestReleaseCutHonorsAdmission(t *testing.T) {
	srv, pool, _, _, _ := testServerHeld(t)
	ctx := t.Context()
	yes := true
	hold := "hold"
	b, e := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Release gate", Columns: []string{"Done"}, TerminalColumns: []string{"Done"}, GateEnabled: &yes, GateOnUnavailable: &hold})
	if e != nil {
		t.Fatal(e)
	}
	r, e := db.SaveRelease(ctx, pool, b.ID, "", db.ReleaseParams{Name: "First", Status: "planned"})
	if e != nil {
		t.Fatal(e)
	}
	title := "Held archive"
	c, e := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ReleaseID: &r.ID}, db.EventMeta{Actor: "human"})
	if e != nil {
		t.Fatal(e)
	}
	token := projectTokenProof(t, "agent", b.ID, boardAgentCaps, true)
	for _, mcp := range []bool{false, true} {
		var resp *http.Response
		if mcp {
			resp = request(t, srv, "POST", "/mcp", token, nil, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_release_close", "arguments": map[string]any{"board_id": b.ID, "release_id": r.ID}}})
			var out struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			decodeBody(t, resp, &out)
			if !out.Result.IsError {
				t.Fatal("MCP bypassed admission")
			}
		} else {
			resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/releases/"+r.ID+"/close", token, nil, map[string]string{})
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("HTTP cut status=%d, want admission hold (202)", resp.StatusCode)
			}
			resp.Body.Close()
		}
		card, e := db.GetCard(ctx, pool, c.Card.ID)
		if e != nil || card.ArchivedAt != nil {
			t.Fatalf("partial cut %+v %v", card, e)
		}
	}
}

func TestMilestoneBurnReadScope(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Milestones"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	token := projectTokenProof(t, "agent", b.ID, boardAgentCaps, false)
	for _, c := range []struct {
		path string
		want int
	}{{"/api/boards/" + b.ID + "/burn?milestone=Launch", 200}, {"/api/boards/" + b.ID + "/burn", 400}, {"/api/boards/" + other.ID + "/burn?milestone=Launch", 404}} {
		resp := request(t, srv, "GET", c.path, token, nil, nil)
		if resp.StatusCode != c.want {
			t.Errorf("%s = %d want %d", c.path, resp.StatusCode, c.want)
		}
		resp.Body.Close()
	}
}
