package daemon

import (
	"context"
	"errors"
	"net/http"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Native questions use the same private message/answer route as the agent
// messaging tool. The server records the answer once; the poll never starts a
// second engine turn or retains the daemon token in the Codex process.
func codexQuestionHandler(base, token string, tokenSource func() string, sessionID string, emitter agent.Emitter, currentTurn ...func() string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, body string) (string, error) {
		if base == "" || (token == "" && tokenSource == nil) {
			return "", errors.New("questions are unavailable for this session")
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type result struct {
			answer string
			failed bool
		}
		answers := make(chan result, 1)
		turnID := ""
		if len(currentTurn) > 0 && currentTurn[0] != nil {
			turnID = currentTurn[0]()
		}
		if childTurn, ok := ctx.Value(codexQuestionOriginKey{}).(string); ok {
			turnID = childTurn
		}
		ownerOnly, _ := ctx.Value(ownerOnlyAsk{}).(bool)
		messenger := &httpMessenger{base: base, token: token, tokenSource: tokenSource, sessionID: sessionID, turnID: turnID, ownerOnly: ownerOnly, client: &http.Client{Timeout: 30 * time.Second}, ctx: ctx, onAnswer: func(answer string) {
			select {
			case answers <- result{answer: answer}:
			case <-ctx.Done():
			}
		}, onPollEnd: func() {
			select {
			case answers <- result{failed: true}:
			default:
			}
		}}
		id, err := messenger.SendAsk(body) //nolint:contextcheck // httpMessenger carries ctx on the instance for posting and polling.
		if err != nil {
			if ownerOnly {
				emitter.Emit(agent.Event{Ts: time.Now(), Kind: "error", Payload: agent.ErrorPayload{Message: ownerOnlyAskFailure(err)}})
			}
			return "", errors.New("the question could not be delivered")
		}
		emitter.Emit(agent.Event{ClientEventID: protocol.SessionMessageEventID(id, "ask"), Ts: time.Now(), Kind: "ask", Payload: agent.MessagingPayload{Body: body, MessageID: id, TurnID: turnID, OwnerOnly: ownerOnly}})
		select {
		case reply := <-answers:
			if reply.failed {
				messenger.CloseAsk(id, "") //nolint:contextcheck // Closing the UI card must outlive the canceled question context.
				return "", errors.New("the question is no longer available")
			}
			answer := reply.answer
			if ctx.Err() != nil {
				messenger.CloseAsk(id, "") //nolint:contextcheck // Closing the UI card must outlive the canceled question context.
				return "", errors.New("question interrupted")
			}
			return answer, nil
		case <-ctx.Done():
			messenger.CloseAsk(id, "") //nolint:contextcheck // Closing the UI card must outlive the canceled question context.
			return "", errors.New("question interrupted")
		}
	}
}

// questionAsker is the AskUser of a Codex or Hermes session: native questions and approvals go to
// the session's chat with the marker code taken out (scrubQuestions). currentTurn may be nil.
func (h *AgentHost) questionAsker(sessionID string, emitter agent.Emitter, nonce string, currentTurn func() string, initialTokens ...string) func(context.Context, string) (string, error) {
	initial := ""
	if len(initialTokens) > 0 {
		initial = initialTokens[0]
	}
	tokenSource := h.nativeHTTPToken(protocol.SpawnSession{SessionID: sessionID, SessionToken: initial})
	return scrubQuestions(codexQuestionHandler(h.cfg.ServerHTTP, "", tokenSource, sessionID, emitter, currentTurn), nonce)
}

// scrubQuestions takes the session's marker code out of a question before it is posted.
func scrubQuestions(ask func(context.Context, string) (string, error), nonce string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, body string) (string, error) {
		return ask(ctx, agent.ScrubCode(body, nonce))
	}
}

// ownerOnlyAskFailure says why an owner-only approval was denied without being asked: the
// server's 409 means the session has no signed-in owner (a runner-key session); anything else
// (a network error, a timeout, a server error, an ended session) is a delivery failure.
func ownerOnlyAskFailure(err error) string {
	var apiErr *messagesAPIError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusConflict {
		return "Approval denied: runner-key sessions cannot answer owner-only approvals; start the session while signed in to answer approvals."
	}
	return "Approval denied: it could not be delivered to the chat (the server could not be reached or refused it)."
}

// ownerOnlyAsk marks a question's context: the server then accepts its answer
// only from the session's signed-in owner (an approval, not a plain question).
type ownerOnlyAsk struct{}

func withOwnerOnlyAsk(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerOnlyAsk{}, true)
}
