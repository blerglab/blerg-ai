package api_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
)

func TestApprovedMetadataIsBoundAndConsumedOnce(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	conn := liveSubscribe(t, srv, b.ID, humanToken(t, srv))
	defer conn.Close()
	name, desc, key := "After", "New description", "NEW"
	d := boardfiling.MetadataPatch{BoardID: b.ID, Name: &name, Description: &desc, Key: &key}
	capability, err := boardfiling.MetadataCapability(newBoardIntent, boardfiling.MetadataDigest(d))
	if err != nil {
		t.Fatal(err)
	}
	tok := newBoardToken(t, newBoardIntent, b.ID, []string{capability})
	// A mutated field cannot use the approved credential.
	changed := d
	other := "Wrong"
	changed.Name = &other
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/metadata-approved", tok, nil, changed)
	if resp.StatusCode != 403 {
		t.Fatalf("mutation=%d", resp.StatusCode)
	}
	resp.Body.Close()
	// No general administration comes with a metadata capability.
	resp = request(t, srv, "PATCH", "/api/boards/"+b.ID, tok, nil, map[string]any{"name": "Wrong"})
	if resp.StatusCode != 403 {
		t.Fatalf("general update=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/metadata-approved", tok, nil, d)
	if resp.StatusCode != 200 {
		t.Fatalf("update=%d", resp.StatusCode)
	}
	resp.Body.Close()
	expectPing(t, conn, "approved metadata", "board_changed")
	got, err := db.GetBoard(t.Context(), pool, b.ID)
	if err != nil || got.Name != name || got.Key != key || got.Description == nil || *got.Description != desc {
		t.Fatalf("board=%+v err=%v", got, err)
	}
	_, err = db.UpdateBoard(t.Context(), pool, b.ID, db.BoardParams{Name: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/metadata-approved", tok, nil, d)
	if resp.StatusCode != 409 {
		t.Fatalf("replay=%d", resp.StatusCode)
	}
	resp.Body.Close()
	got, _ = db.GetBoard(t.Context(), pool, b.ID)
	if got.Name != "Later" {
		t.Fatal("replay overwrote later edit")
	}
	old, err := db.BoardIDForKey(t.Context(), pool, b.Key)
	if err != nil || old != b.ID {
		t.Fatal("old alias lost")
	}
}

func TestApprovedConfigurationIsAtomicAndBoardScoped(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Configuration"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := db.ListColumns(t.Context(), pool, b.ID)
	foreign, _ := db.ListColumns(t.Context(), pool, other.ID)
	patch := func(raw string) boardfiling.MetadataPatch {
		var d boardfiling.MetadataPatch
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		d.BoardID = b.ID
		return d
	}
	intent := newBoardIntent
	apply := func(d boardfiling.MetadataPatch, want int) {
		capability, err := boardfiling.MetadataCapability(intent, boardfiling.MetadataDigest(d))
		if err != nil {
			t.Fatal(err)
		}
		tok := newBoardToken(t, intent, b.ID, []string{capability})
		r := request(t, srv, "POST", "/api/boards/"+b.ID+"/metadata-approved", tok, nil, d)
		defer r.Body.Close()
		if r.StatusCode != want {
			body, _ := io.ReadAll(r.Body)
			t.Fatalf("status=%d want=%d: %s", r.StatusCode, want, body)
		}
	}
	// A foreign column must fail the whole patch and leave the one-use intent unconsumed.
	apply(patch(`{"name":"Wrong metadata","require_repo":true,"columns":[{"id":"`+foreign[0].ID+`","name":"Wrong"}]}`), 404)
	unchanged, err := db.GetBoard(t.Context(), pool, b.ID)
	if err != nil || unchanged.Name != "Configuration" || unchanged.RequireRepo {
		t.Fatalf("partial configuration update=%+v err=%v", unchanged, err)
	}
	// A populated column cannot be deleted through a metadata approval.
	_, err = pool.Exec(t.Context(), `INSERT INTO cards(board_id,column_id,number,title,type,rank) VALUES($1,$2,1,'Card','task','a')`, b.ID, cols[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO card_tags(card_id,tag) SELECT id,tag FROM cards CROSS JOIN (VALUES('old'),('new')) AS tags(tag) WHERE board_id=$1`, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	apply(patch(`{"columns":[{"id":"`+cols[0].ID+`","delete":true}]}`), 409)
	if _, err = pool.Exec(t.Context(), `INSERT INTO cards(board_id,column_id,number,title,type,rank) VALUES($1,$2,1,'Other card','task','a')`, other.ID, foreign[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO card_tags(card_id,tag) SELECT id,'old' FROM cards WHERE board_id=$1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE cards SET archived_at=now() WHERE board_id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	apply(patch(`{"columns":[{"id":"`+cols[0].ID+`","delete":true}]}`), 409)
	apply(patch(`{"deploy_url":"https://example.com","include_component_version":false,"repos":["team/app"],"require_repo":false,"field_schema":[{"key":"area","type":"text"}],"columns":[{"id":"`+cols[0].ID+`","name":"Queue","is_terminal":true,"before_id":""},{"name":"Review"}],"tags":[{"from":"old","to":"new"}]}`), 200)
	got, _ := db.GetBoard(t.Context(), pool, b.ID)
	if got.DeployURL == nil || *got.DeployURL != "https://example.com" || got.IncludeComponentVersion || got.RequireRepo || len(got.Repos) != 1 || got.Repos[0] != "team/app" || !strings.Contains(string(got.FieldSchema), "area") {
		t.Fatalf("board=%+v", got)
	}
	updated, _ := db.ListColumns(t.Context(), pool, b.ID)
	if len(updated) != len(cols)+1 || (updated[len(updated)-2].Name != "Queue" || !updated[len(updated)-2].IsTerminal) || updated[len(updated)-1].Name != "Review" {
		t.Fatalf("columns=%+v", updated)
	}
	var tag string
	var version int
	if err := pool.QueryRow(t.Context(), `SELECT t.tag,c.version FROM card_tags t JOIN cards c ON c.id=t.card_id WHERE c.board_id=$1`, b.ID).Scan(&tag, &version); err != nil || tag != "new" || version != 1 {
		t.Fatalf("tag=%s version=%d err=%v", tag, version, err)
	}
	events, err := db.ListColumnEvents(t.Context(), pool, b.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events[0].Type != "updated" || events[1].Type != "moved" || events[2].Type != "created" {
		t.Fatalf("audit types=%+v", events)
	}
	var createdData map[string]any
	if json.Unmarshal(events[2].Data, &createdData) != nil || createdData["name"] != "Review" || createdData["is_terminal"] != false {
		t.Fatalf("created audit=%s", events[2].Data)
	}
	if events[0].Actor != "human" || events[0].ActorAccount == "" {
		t.Fatalf("approval actor=%+v", events[0])
	}
	var foreignTag string
	if err := pool.QueryRow(t.Context(), `SELECT t.tag FROM card_tags t JOIN cards c ON c.id=t.card_id WHERE c.board_id=$1`, other.ID).Scan(&foreignTag); err != nil || foreignTag != "old" {
		t.Fatalf("foreign tag=%s err=%v", foreignTag, err)
	}
	// The intent also protects settings and card tags from replay.
	apply(patch(`{"require_repo":true}`), 409)
	// A fresh approval can remove a tag and an empty column, and explicitly clear settings.
	intent = other.ID
	apply(patch(`{"deploy_url":"","include_component_version":true,"repos":[],"field_schema":[],"tags":[{"from":"new","delete":true}],"columns":[{"id":"`+cols[1].ID+`","delete":true},{"id":"`+cols[0].ID+`","is_terminal":false,"before_id":"`+cols[2].ID+`"}]}`), 200)
	got, _ = db.GetBoard(t.Context(), pool, b.ID)
	if got.DeployURL == nil || *got.DeployURL != "" || !got.IncludeComponentVersion || len(got.Repos) != 0 || string(got.FieldSchema) != "[]" {
		t.Fatalf("clear=%+v", got)
	}
	updated, _ = db.ListColumns(t.Context(), pool, b.ID)
	if len(updated) != len(cols) || updated[0].ID != cols[0].ID || updated[0].IsTerminal {
		t.Fatalf("delete/reorder=%+v", updated)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM card_tags t JOIN cards c ON c.id=t.card_id WHERE c.board_id=$1`, b.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("tags=%d err=%v", remaining, err)
	}

}
