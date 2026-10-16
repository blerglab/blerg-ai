package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestAcceptedAskAnswerCommitsTranscriptAndReceiptTogether(t *testing.T) {
	f := newIngestFixture(t)
	askID, err := db.CreateMessage(f.ctx, f.pool, f.sessionID, "ask", "Which option?")
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(f.ctx, f.hub, f.pool, f.sessionID, messageEventID(askID, "ask"), "ask", messagingPayload{Body: "Which option?", MessageID: askID})
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "First option", ClientID: "ask-input", AnswerMessageID: askID}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	// Refuse the transcript write: the answer and receipt must both remain uncommitted.
	_, err = f.pool.Exec(f.ctx, `CREATE FUNCTION refuse_answer_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='user_message' THEN RAISE EXCEPTION 'injected answer event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_answer_event BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION refuse_answer_event()`)
	if err != nil {
		t.Fatal(err)
	}
	if !answerAskFromChat(f.ctx, f.hub, f.pool, f.sessionID, msg.Text, msg.ClientID, askID) {
		t.Fatal("failed ask transaction must not fall through to engine")
	}
	ask, err := db.GetMessage(f.ctx, f.pool, askID)
	if err != nil || ask.Status != "open" {
		t.Fatalf("answer committed without transcript: %+v %v", ask, err)
	}
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("input must remain pending: %+v %v", pending, err)
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER refuse_answer_event ON agent_events; DROP FUNCTION refuse_answer_event()`); err != nil {
		t.Fatal(err)
	}
	if !answerAskFromChat(f.ctx, f.hub, f.pool, f.sessionID, msg.Text, msg.ClientID, askID) {
		t.Fatal("retry did not answer")
	}
	ask, err = db.GetMessage(f.ctx, f.pool, askID)
	if err != nil || ask.Status != "answered" {
		t.Fatalf("answer not committed: %+v %v", ask, err)
	}
	pending, err = db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("committed answer must settle input: %+v %v", pending, err)
	}
}

// Shared IDs opt daemon Codex questions into server answer routing. Native
// Claude-loop asks retain their own IDs and answer handling.
func TestCodexTypedNativeQuestionRoutesOneAnswerToPoll(t *testing.T) {
	f := newIngestFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='codex' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	post := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{"session_id":"`+f.sessionID+`","kind":"ask","body":"Pick a color","turn_id":"native-question-turn"}`))
	post.Header.Set("Authorization", "Bearer fixture-daemon-token")
	posted := httptest.NewRecorder()
	api.HandlePostMessages(posted, post)
	var created struct {
		ID string `json:"id"`
	}
	if posted.Code != http.StatusCreated || json.Unmarshal(posted.Body.Bytes(), &created) != nil || created.ID == "" {
		t.Fatal("native question creation failed")
	}
	askID := created.ID
	// The daemon emits the same ask after SendAsk returns. Its event must dedupe.
	payload, _ := json.Marshal(messagingPayload{Body: "Pick a color", MessageID: askID, TurnID: "native-question-turn"})
	HandleAgentEvent(f.ctx, f.hub, f.pool, f.dc, protocol.AgentEvent{Type: "agent_event", SessionID: f.sessionID, ClientEventID: protocol.SessionMessageEventID(askID, "ask"), Kind: "ask", Payload: payload, Ts: time.Now().UTC().Format(time.RFC3339Nano)})
	input := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "Blue", ClientID: "native-question-answer"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, input); err != nil {
		t.Fatal(err)
	}
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("typed answer not settled: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/messages/"+askID+"/answer", nil)
	req.SetPathValue("id", askID)
	req.Header.Set("Authorization", "Bearer fixture-daemon-token")
	rec := httptest.NewRecorder()
	api.HandleGetMessageAnswer(rec, req)
	var answer answerResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &answer) != nil || !answer.Answered || answer.Answer == nil || *answer.Answer != "Blue" {
		t.Fatal("answer poll did not deliver typed reply")
	}
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	var count int
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='user_message'`, f.sessionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("answer recorded %d times: %v", count, err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, f.sessionID, protocol.SessionMessageEventID(askID, "answer")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var recorded askAnswerPayload
	if json.Unmarshal(raw, &recorded) != nil || recorded.ClientID != input.ClientID || recorded.TurnID != "native-question-turn" || recorded.MessageID != askID {
		t.Fatal("answer lost client, question, or turn identity")
	}
}

func TestCodexQuestionCreationRollsBackWithoutRoutingMarker(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "fixture-token", nil, "")
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION refuse_native_ask() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='ask' THEN RAISE EXCEPTION 'injected ask event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_native_ask BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION refuse_native_ask()`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.createCodexQuestion(f.ctx, postMessageRequest{SessionID: f.sessionID, Kind: "ask", Body: "Question"}); err == nil {
		t.Fatal("routing marker failure must fail creation")
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM messages WHERE session_id=$1`, f.sessionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unroutable question survived rollback: count=%d err=%v", count, err)
	}
}

func TestCodexHistoricalQuestionAnswerRepairsMissingAskMarker(t *testing.T) {
	f := newIngestFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='codex' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateMessage(f.ctx, f.pool, f.sessionID, "ask", "Historical question")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetMessage(f.ctx, f.pool, id)
	if err != nil || row == nil {
		t.Fatal("question missing")
	}
	recordAskAnswer(f.ctx, f.hub, f.pool, *row, "Blue", "historical-client")
	recordAskAnswer(f.ctx, f.hub, f.pool, *row, "Blue", "historical-client")
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='user_message'`, f.sessionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical answer count=%d err=%v", count, err)
	}
}
