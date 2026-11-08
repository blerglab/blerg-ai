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
	return hostedQuestionHandler(0, base, token, tokenSource, sessionID, emitter, currentTurn...)
}

// hostedQuestionHandler is codexQuestionHandler for one hosting of the session.
func hostedQuestionHandler(generation int64, base, token string, tokenSource func() string, sessionID string, emitter agent.Emitter, currentTurn ...func() string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, body string) (string, error) {
		if base == "" || (token == "" && tokenSource == nil) {
			return "", errors.New("questions are unavailable for this session")
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type result struct {
			answer agent.AskAnswer
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
		messenger := &httpMessenger{lateAnswerRelay: true, hostGeneration: generation, base: base, token: token, tokenSource: tokenSource, sessionID: sessionID, turnID: turnID, ownerOnly: ownerOnly, client: &http.Client{Timeout: 30 * time.Second}, ctx: ctx, onAnswerFrame: func(answer agent.AskAnswer) {
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
			if ctx.Err() != nil {
				messenger.CloseAsk(id, "") //nolint:contextcheck // Closing the UI card must outlive the canceled question context.
				return "", errors.New("question interrupted")
			}
			// Options match the raw answer; only free text can carry a label.
			if free, _ := ctx.Value(freeTextQuestion{}).(bool); free {
				return reply.answer.Label + reply.answer.Text, nil
			}
			return reply.answer.Text, nil
		case <-ctx.Done():
			messenger.CloseAsk(id, "") //nolint:contextcheck // Closing the UI card must outlive the canceled question context.
			return "", errors.New("question interrupted")
		}
	}
}

// NewQuestionHandler is the answer path of a Codex or Hermes session's AskUser.
func NewQuestionHandler(base string, tokenSource func() string, sessionID string, emitter agent.Emitter) func(context.Context, string) (string, error) {
	return codexQuestionHandler(base, "", tokenSource, sessionID, emitter)
}

// freeTextQuestion marks a question with no options to match its answer against.
type freeTextQuestion struct{}

// WithFreeTextQuestion lets a question's answer carry a child's source label.
func WithFreeTextQuestion(ctx context.Context) context.Context {
	return context.WithValue(ctx, freeTextQuestion{}, true)
}

// questionAsker is the AskUser of a Codex or Hermes session: native questions, and Hermes approvals, go to
// the session's chat with the marker code taken out (scrubQuestions). currentTurn may be nil.
func (h *AgentHost) questionAsker(sessionID string, emitter agent.Emitter, nonce string, currentTurn func() string, initialTokens ...string) func(context.Context, string) (string, error) {
	return h.questionAskerAt(0, sessionID, emitter, nonce, currentTurn, initialTokens...)
}

// questionAskerAt is questionAsker for one hosting of the session.
func (h *AgentHost) questionAskerAt(generation int64, sessionID string, emitter agent.Emitter, nonce string, currentTurn func() string, initialTokens ...string) func(context.Context, string) (string, error) {
	initial := ""
	if len(initialTokens) > 0 {
		initial = initialTokens[0]
	}
	tokenSource := h.nativeHTTPToken(protocol.SpawnSession{SessionID: sessionID, SessionToken: initial})
	return scrubQuestions(hostedQuestionHandler(generation, h.cfg.ServerHTTP, "", tokenSource, sessionID, emitter, currentTurn), nonce)
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
