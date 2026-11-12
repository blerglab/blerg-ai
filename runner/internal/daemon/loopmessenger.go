package daemon

import (
	"context"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

// LoopMessengerConfig wires the native loop to the session's private message API.
type LoopMessengerConfig struct {
	Context                         context.Context
	BaseURL, SessionID, MarkerNonce string
	TokenSource                     func() string
	OnAnswer                        func(agent.AskAnswer)
	OwnerOnly                       bool
	HostGeneration                  int64
}

func NewLoopMessenger(c LoopMessengerConfig) agent.Messenger {
	m := &httpMessenger{base: c.BaseURL, sessionID: c.SessionID, markerNonce: c.MarkerNonce, tokenSource: c.TokenSource, ctx: c.Context, client: &http.Client{Timeout: 60 * time.Second}, ownerOnly: c.OwnerOnly, hostGeneration: c.HostGeneration}
	m.onAnswerFrame = c.OnAnswer
	return m
}
