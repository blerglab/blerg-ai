package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/chatproxy"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// The chat proxy: what the web app's chat (the @blerglab/chat package, over its
// proxy transport) talks to. A card's session and the board session are shown
// by the same chat the runner's own app uses — live text, tool groups, files,
// review and mark-up — and that chat needs a session's live stream and files,
// which the board does not ingest. So these routes pass each request through
// to the runner, for ONE session, after the board has decided the caller may
// see it. The browser still never talks to the runner and never sees the
// runner credential: the board brokers, as it always has.
//
// {id} is the board's own runner-session id (runner_sessions.id), the id every
// other session route uses; the proxy maps it to the runner's.
//
// Reads need card.read on the session's board; everything that changes the
// session needs card.write. Sending a message and attaching a file are a
// person's acts and are refused to anything else: the runner records them as
// the person's ("human"), and an agent or the service key has
// POST /api/runner-sessions/{id}/message, which says who it is.

// chatAllowed marks a request the board has already authorized; the proxy's own
// Authorize only checks that the mark is there, so a route reached any other
// way is refused.
type chatAllowed struct{}

func (a *API) chatRoutes(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	inner := http.NewServeMux()
	chatproxy.MountWith(inner, "/api/chat", chatRunner{a}, chatproxy.Options{
		Authorize: func(r *http.Request, _ string) (bool, error) {
			return r.Context().Value(chatAllowed{}) != nil, nil
		},
		Resolve: func(r *http.Request, id string) (string, error) {
			ext, err := a.runnerExternalID(r.Context(), id)
			if err != nil {
				return "", chatproxy.ErrNotFound
			}
			return ext, nil
		},
		MessageSource: "human",
	})
	read := a.chatGate(inner, "card.read", false)
	write := a.chatGate(inner, "card.write", false)
	person := a.chatGate(inner, "card.write", true)

	mux.HandleFunc("GET /api/chat/sessions/{id}", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/events/live", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/events", authed(read))
	mux.HandleFunc("POST /api/chat/sessions/{id}/messages", authed(person))
	mux.HandleFunc("POST /api/chat/sessions/{id}/stop", authed(write))
	mux.HandleFunc("POST /api/chat/sessions/{id}/interrupt", authed(write))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts/{aid}/raw", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts/{aid}/download", authed(read))
	mux.HandleFunc("DELETE /api/chat/sessions/{id}/artifacts/{aid}", authed(write))
	mux.HandleFunc("POST /api/chat/sessions/{id}/uploads", authed(person))
}

// chatGate is the board's decision in front of one proxied route: the session
// exists (404 otherwise, for every principal), the caller holds the capability
// on its board, and — for a person's act — the caller is a person.
func (a *API) chatGate(inner http.Handler, capability string, personOnly bool) func(http.ResponseWriter, *http.Request, auth.Principal) {
	return func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		boardID, err := a.sessionBoardID(r.Context(), r.PathValue("id"))
		if err != nil {
			writeDBError(w, err)
			return
		}
		if err := p.RequireBoard(boardID, capability); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if personOnly && !p.IsHuman() {
			writeError(w, http.StatusForbidden, "only a signed-in person sends through the chat; use POST /api/runner-sessions/{id}/message")
			return
		}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chatAllowed{}, true)))
	}
}

// chatRunner is the proxy's runner client: the configured driver, when it can
// pass a request through.
type chatRunner struct{ a *API }

func (c chatRunner) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if c.a.runner == nil {
		return nil, errors.New("no runner configured")
	}
	raw, ok := c.a.runner.Driver.(runner.RawDoer)
	if !ok {
		return nil, errors.New("this runner driver cannot proxy a session")
	}
	return raw.Raw(ctx, method, path, body, headers)
}
