package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (a *API) HandleShellConsent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if !shellOrigin(r) {
		writeError(w, 403, "forbidden origin")
		return
	}
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if a.shellRuntime == nil {
		writeError(w, 503, "shell runtime unavailable")
		return
	}
	b, err := (shellResolver{a, a.shellRuntime}).Resolve(ctx, r.PathValue("id"))
	if err != nil || b.Owner != actor.Owner || b.Runtime != "host" || !b.Live {
		writeError(w, 403, "shell unavailable")
		return
	}
	authority := shellAuthority{a.coreURL, a.coreInternalKey, &http.Client{Timeout: 2 * time.Second}}
	policy, err := authority.Authorize(ctx, actor)
	if err != nil || !policy.Enabled {
		writeError(w, 403, "shell unavailable")
		return
	}
	description, err := a.shellRuntime.describe(ctx, b.Session)
	if err != nil || description.Binding.RuntimeGeneration != b.RuntimeGeneration || description.Binding.Root != b.Root {
		writeError(w, 403, "runtime changed")
		return
	}
	binding := consentBinding(b, actor.Login, description.Identity)
	w.Header().Set("Cache-Control", "no-store")
	if r.PathValue("action") == "challenge" {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 2))
		if err != nil || len(raw) != 0 {
			writeError(w, 400, "body must be empty")
			return
		}
		i := description.Identity
		consent := protocol.HostShellConsent{ID: shellRandom(), OwnerID: actor.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: shellRandom()}
		if consent.ID == "" || consent.ChallengeID == "" || db.CreateShellChallenge(ctx, a.dbPool, binding, consent, time.Now().Add(2*time.Minute)) != nil {
			writeError(w, 503, "challenge unavailable")
			return
		}
		writeJSON(w, 201, consent)
		return
	}
	if r.PathValue("action") != "accept" {
		writeError(w, 404, "not found")
		return
	}
	var request struct {
		ChallengeID string `json:"challenge_id"`
		Accept      bool   `json:"accept"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || !request.Accept || request.ChallengeID == "" || decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "affirmative challenge required")
		return
	}
	consent, err := db.AcceptShellChallenge(ctx, a.dbPool, binding, request.ChallengeID)
	if err != nil {
		writeError(w, 403, "challenge expired or runtime changed")
		return
	}
	writeJSON(w, 200, consent)
}
