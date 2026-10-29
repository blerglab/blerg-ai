package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

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
type chatOwnerProof struct{}

// chatAccount carries the signed-in person's core account id to the proxy's Account option.
type chatAccount struct{}

// chatActing carries the person a file change is for (the proxy's Acting option): the signed-in
// person the gate has just let change the session, and whether core made them an administrator.
type chatActing struct {
	account string
	admin   bool
}

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
		Account:       func(r *http.Request) string { s, _ := r.Context().Value(chatAccount{}).(string); return s },
		// The runner sees the board's shared key; it decides deletes, storage and upload
		// attribution for the person named here (#231).
		Acting: func(r *http.Request) (string, bool) {
			a, _ := r.Context().Value(chatActing{}).(chatActing)
			return a.account, a.admin
		},
	})
	read := a.chatGate(inner, "card.read", false)
	write := a.chatGate(inner, "card.write", false)
	person := a.chatGate(inner, "card.write", true)

	mux.HandleFunc("GET /api/chat/sessions/{id}", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/events/live", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/events", authed(read))
	mux.HandleFunc("POST /api/chat/sessions/{id}/messages", authed(person))
	mux.HandleFunc("POST /api/chat/sessions/{id}/model", authed(person))
	mux.HandleFunc("POST /api/chat/sessions/{id}/stop", authed(write))
	mux.HandleFunc("POST /api/chat/sessions/{id}/interrupt", authed(write))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts/{aid}/raw", authed(read))
	mux.HandleFunc("GET /api/chat/sessions/{id}/artifacts/{aid}/download", authed(read))
	mux.HandleFunc("DELETE /api/chat/sessions/{id}/artifacts/{aid}", authed(write))
	mux.HandleFunc("POST /api/chat/sessions/{id}/uploads", authed(person))
	mux.HandleFunc("POST /api/chat/sessions/{id}/storage", authed(person))
	// The person's display preferences (their code colours in Blerg Runner), read so the chat shows
	// code in them. No board is involved, so there is no capability to hold; only a signed-in person
	// (a core human token) has an account to read, anything else gets the defaults ({}).
	mux.HandleFunc("GET /api/chat/preferences", authed(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		ctx := context.WithValue(r.Context(), chatAllowed{}, true)
		if p.FromCore && p.IsHuman() && p.Token != nil {
			ctx = context.WithValue(ctx, chatAccount{}, strings.TrimPrefix(p.Token.ID, "core:"))
		}
		inner.ServeHTTP(w, r.WithContext(ctx))
	}))
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
		ctx := context.WithValue(r.Context(), chatAllowed{}, true)
		// The person the board vouches for on the runner's file routes: only a signed-in person,
		// and only past this gate. An agent or service principal names nobody.
		if account := p.HumanAccountID(); account != "" {
			ctx = context.WithValue(ctx, chatActing{}, chatActing{account: account, admin: p.Token != nil && p.Token.HasCap("account.manage")})
		}
		// Reads use the board service identity; controls that may recover a
		// conversation carry a fresh owner proof, never a stored launch token.
		if r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/messages") || strings.HasSuffix(r.URL.Path, "/model")) {
			if a.runner == nil {
				writeError(w, http.StatusServiceUnavailable, "no runner configured")
				return
			}
			if _, ok := a.runner.Driver.(runner.IdentityRawDoer); ok {
				token, err := a.startIdentity(ctx, boardID)
				if err != nil {
					writeStartError(w, err)
					return
				}
				ctx = context.WithValue(ctx, chatOwnerProof{}, token)
			}
		}
		inner.ServeHTTP(w, r.WithContext(ctx))
	}
}

// chatRunner is the proxy's runner client: the configured driver, when it can
// pass a request through.
type chatRunner struct{ a *API }

func (c chatRunner) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if c.a.runner == nil {
		return nil, errors.New("no runner configured")
	}
	if token, ok := ctx.Value(chatOwnerProof{}).(string); ok {
		raw, supported := c.a.runner.Driver.(runner.IdentityRawDoer)
		if !supported {
			return nil, errors.New("runner cannot carry recovery identity")
		}
		return raw.RawAs(ctx, token, method, path, body, headers)
	}
	raw, ok := c.a.runner.Driver.(runner.RawDoer)
	if !ok {
		return nil, errors.New("this runner driver cannot proxy a session")
	}
	return raw.Raw(ctx, method, path, body, headers)
}
