package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestChannelRecommendationKeep(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	db.AppendAgentEvent(t.Context(), fx.pool, fx.parent, newUUID(), "user_message", `{"text":"Write a TDD skill"}`)
	path := "/api/sessions/" + fx.parent + "/channel-recommendations"
	input := map[string]any{"topic": "TDD skill", "from_seq": 1, "to_seq": 1, "context": "Develop a skill"}
	status, body, _ := fx.do("POST", path, fx.parentTok, input, nil)
	if status != 201 {
		t.Fatalf("recommend: %d %v", status, body)
	}
	id := asString(body["id"])
	status, _, _ = fx.do("POST", path+"/"+id+"/decide", fx.parentTok, map[string]any{"action": "approve"}, nil)
	if status == 200 || status == 202 {
		t.Fatal("agent approved")
	}
	status, body, _ = fx.do("POST", path+"/"+id+"/decide", fx.human("owner-1"), map[string]any{"action": "keep"}, nil)
	if status != 200 || body["state"] != "kept" {
		t.Fatalf("keep: %d %v", status, body)
	}
	input["topic"] = "  tdd SKILL "
	status, body, _ = fx.do("POST", path, fx.parentTok, input, nil)
	if status != 200 || body["id"] != id || body["state"] != "kept" {
		t.Fatalf("repeat: %d %v", status, body)
	}
}

func TestChannelRecommendationApproveSeedsOnlySelectedMessages(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	for _, text := range []string{"Unrelated earlier message", "Write a TDD skill", "Unrelated later message"} {
		raw, _ := json.Marshal(map[string]string{"text": text})
		if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.parent, newUUID(), "user_message", string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/sessions/" + fx.parent + "/channel-recommendations"
	code, rec, _ := fx.do("POST", path, fx.parentTok, map[string]any{"topic": "TDD skill", "from_seq": 2, "to_seq": 2, "context": "Create a reusable skill"}, nil)
	if code != 201 {
		t.Fatalf("recommend %d %v", code, rec)
	}
	id := asString(rec["id"])
	for _, token := range []string{fx.parentTok, fx.human("other")} {
		code, _, _ = fx.do("POST", path+"/"+id+"/decide", token, map[string]any{"action": "approve"}, nil)
		if code < 400 {
			t.Fatal("nonowner approved")
		}
	}
	code, result, _ := fx.do("POST", path+"/"+id+"/decide", fx.human("owner-1"), map[string]any{"action": "approve"}, nil)
	if code != 200 || result["state"] != "approved" {
		t.Fatalf("approve %d %v", code, result)
	}
	spawn := fx.spawn()
	if !strings.Contains(spawn.InitialPrompt, "Write a TDD skill") || !strings.Contains(spawn.InitialPrompt, "Create a reusable skill") || strings.Contains(spawn.InitialPrompt, "Unrelated") {
		t.Fatalf("seed: %s", spawn.InitialPrompt)
	}
	code, again, _ := fx.do("POST", path+"/"+id+"/decide", fx.human("owner-1"), map[string]any{"action": "approve"}, nil)
	if code != 200 || again["channel_id"] != result["channel_id"] {
		t.Fatalf("replay %d %v", code, again)
	}
}

func TestChannelRecommendationClosingOutcome(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_via='person' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	raw, _ := json.Marshal(channelRecommendation{ID: newUUID(), Topic: "TDD skill", State: "approved", ChannelID: child})
	if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.parent, newUUID(), "channel_recommendation", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, child, newUUID(), "assistant_text", `{"text":"The skill is ready.","done":true}`); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child})
	note := fx.parentNote(fx.parent, 5*time.Second)
	if strings.Contains(note.Text, "The skill is ready.") || !strings.Contains(note.Text, "kind=channel_closed") || !strings.Contains(note.Text, child) {
		t.Fatalf("outcome: %+v", note)
	}
	// Turning a recommended channel private must prevent even its summary reaching Main.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if !fx.api.noteChildTurn(t.Context(), child, "recommended_channel_closed", "") {
		t.Fatal("private channel should be skipped")
	}
}

func TestChannelRecommendationRejectsInvalidInput(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.RegisterCategoryRoutes(fx.mux)
	path := "/api/sessions/" + fx.parent + "/channel-recommendations"
	for _, input := range []map[string]any{
		{"topic": "TDD", "context": "why", "from_seq": 0, "to_seq": 0},
		{"topic": "TDD", "context": "why", "from_seq": 2, "to_seq": 1},
		{"topic": "TDD", "context": "why", "from_seq": 1, "to_seq": 102},
		{"topic": "TDD", "context": strings.Repeat("x", 4097), "from_seq": 1, "to_seq": 1},
		{"topic": "", "context": "why", "from_seq": 1, "to_seq": 1},
		{"topic": "TDD", "context": "why", "from_seq": 1, "to_seq": 1}, // no message in this range
	} {
		code, body, _ := fx.do("POST", path, fx.parentTok, input, nil)
		if code != 422 {
			t.Fatalf("invalid recommendation: %d %v", code, body)
		}
	}
	child, tok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	input := map[string]any{"topic": "TDD", "context": "why", "from_seq": 1, "to_seq": 1}
	for _, test := range []struct{ path, token string }{{path, tok}, {path, fx.human("owner-1")}, {"/api/sessions/" + child + "/channel-recommendations", tok}} {
		code, body, _ := fx.do("POST", test.path, test.token, input, nil)
		if code < 400 {
			t.Fatalf("non Main recommendation: %d %v", code, body)
		}
	}
}
