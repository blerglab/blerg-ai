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
func codexQuestionHandler(base, token, sessionID string, emitter agent.Emitter, currentTurn ...func() string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, body string) (string, error) {
		if base == "" || token == "" {
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
		messenger := &httpMessenger{base: base, token: token, sessionID: sessionID, turnID: turnID, ownerOnly: ownerOnly, client: &http.Client{Timeout: 30 * time.Second}, ctx: ctx, onAnswer: func(answer string) {
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
			return "", errors.New("the question could not be delivered")
		}
		emitter.Emit(agent.Event{ClientEventID: protocol.SessionMessageEventID(id, "ask"), Ts: time.Now(), Kind: "ask", Payload: agent.MessagingPayload{Body: body, MessageID: id, TurnID: turnID}})
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

// ownerOnlyAsk marks a question's context: the server then accepts its answer
// only from the session's signed-in owner (an approval, not a plain question).
type ownerOnlyAsk struct{}

func withOwnerOnlyAsk(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerOnlyAsk{}, true)
}
