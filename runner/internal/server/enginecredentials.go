package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

var errPersonalCredentialUnavailable = errors.New("the owner’s credential could not be verified; retry or reconnect in Settings")

var errManagedCredential = errors.New("the managed engine connection is unavailable; reconnect it in Settings")

// managedAccess is access-only. Refresh authority stays encrypted in Core.
type managedAccess = engineaccess.Access

type managedAccessRequest struct {
	AccountID          string `json:"account_id"`
	Engine             string `json:"engine"`
	TokenID            string `json:"token_id,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	Force              bool   `json:"force,omitempty"`
	PreviousGeneration string `json:"previous_generation,omitempty"`
}

// fetchManagedAccess only permits opaque legacy fallback on Core's explicit
// not_managed response. Transport, authorization and refresh failures fail closed.
func fetchManagedAccess(parent context.Context, client *http.Client, coreURL, key string, body managedAccessRequest) (*managedAccess, bool, error) {
	if coreURL == "" || key == "" || body.AccountID == "" || !(coreProof{TokenID: body.TokenID, SessionID: body.SessionID}).valid() {
		return nil, false, errManagedCredential
	}
	ctx, cancel := context.WithTimeout(parent, 7*time.Second)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, false, errManagedCredential
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/credentials/managed/access", bytes.NewReader(raw))
	if err != nil {
		return nil, false, errManagedCredential
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", key)
	resp, err := coreHTTPClient(client).Do(req)
	if err != nil {
		return nil, false, errManagedCredential
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 32<<10+1))
	if err != nil || len(raw) > 32<<10 {
		return nil, false, errManagedCredential
	}
	if resp.StatusCode == http.StatusConflict {
		var status struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &status) == nil && status.Code == "not_managed" {
			return nil, false, nil
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, errManagedCredential
	}
	access, err := engineaccess.DecodeForEngine(body.Engine, raw, time.Now())
	if err != nil {
		return nil, false, errManagedCredential
	}
	return &access, true, nil
}

// HandleManagedCredentialAccess accepts only the current pod's runtime token:
// the daemon's BLERG_RUNNER_DAEMON_TOKEN, which no agent process inherits.
// The session's messaging token lives in the agent's own environment (so that
// blerg-runner works), so it is refused with 403. Account, engine and Core
// principal are read from the private launch binding. Browser, daemon master
// and caller-supplied account identifiers grant no access.
func (a *API) HandleManagedCredentialAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int) { writeError(w, status, "managed engine connection unavailable") }
	if a.dbPool == nil {
		fail(http.StatusServiceUnavailable)
		return
	}
	raw := a.rawBearer(r)
	if raw == "" || a.isDaemonToken(raw) {
		fail(http.StatusUnauthorized)
		return
	}
	token, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw)
	if err != nil {
		if session, e := db.ValidateBoardToken(r.Context(), a.dbPool, raw); e == nil && session.BoardID == "" && slices.Contains(session.Capabilities, "message") {
			fail(http.StatusForbidden)
			return
		}
		fail(http.StatusUnauthorized)
		return
	}
	var binding managedAccessRequest
	var boundRuntimeToken string
	err = a.dbPool.QueryRow(r.Context(), `SELECT m.account_id,m.engine,m.token_id,m.auth_session_id,m.runtime_token_id::text
 FROM session_managed_credential m JOIN sessions s ON s.id=m.session_id
 WHERE m.session_id=$1 AND s.ended_at IS NULL AND s.runtime='cluster' AND s.spawning_account_id=m.account_id AND s.engine=m.engine`, token.SessionID).Scan(&binding.AccountID, &binding.Engine, &binding.TokenID, &binding.SessionID, &boundRuntimeToken)
	if err != nil || boundRuntimeToken != token.ID {
		fail(http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var request struct {
		Force              bool   `json:"force"`
		PreviousGeneration string `json:"previous_generation"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(request.PreviousGeneration) > 128 || (request.Force && request.PreviousGeneration == "") {
		fail(http.StatusBadRequest)
		return
	}
	binding.Force = request.Force
	binding.PreviousGeneration = request.PreviousGeneration
	access, found, err := fetchManagedAccess(r.Context(), nil, a.coreURL, a.coreInternalKey, binding)
	if err != nil || !found {
		fail(http.StatusServiceUnavailable)
		return
	}
	// Recheck runtime authority after the provider call. A replacement pod or stop
	// may have revoked this token while Core renewed its connection.
	current, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw)
	if err != nil || current.ID != token.ID {
		fail(http.StatusForbidden)
		return
	}
	var live bool
	err = a.dbPool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM session_managed_credential m JOIN sessions s ON s.id=m.session_id WHERE m.session_id=$1 AND m.runtime_token_id=$2 AND s.ended_at IS NULL AND s.runtime='cluster' AND s.spawning_account_id=m.account_id AND s.engine=m.engine)`, token.SessionID, token.ID).Scan(&live)
	if err != nil || !live {
		fail(http.StatusForbidden)
		return
	}
	// The runtime token slides on the pod's heartbeat, not here.
	writeJSON(w, http.StatusOK, access)
}

var errCoreManagedCredential = errors.New("the engine credential requires managed access")

const managedCredentialEnv = "BLERG_RUNNER_MANAGED_CREDENTIAL" //nolint:gosec // G101: environment variable name, never a credential.

func (j *JobManager) bindManagedCredential(ctx context.Context, spec SessionJobSpec) error {
	if !engineaccess.SupportsEngine(spec.Engine) || j.historyPool == nil {
		return errManagedCredential
	}
	_, found, err := fetchManagedAccess(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, managedAccessRequest{AccountID: spec.SpawningAccountID, Engine: spec.Engine, TokenID: spec.TokenID, SessionID: spec.AuthSessionID})
	if err != nil || !found {
		return errManagedCredential
	}
	// Bind the launch's runtime token, never the messaging token: only the
	// pod's daemon holds the runtime token; the agent's environment does not.
	token, err := db.ValidateRuntimeToken(ctx, j.historyPool, spec.ExtraEnv[runtimeTokenEnv])
	if err != nil || token.SessionID != spec.SessionID {
		return errManagedCredential
	}
	tag, err := j.historyPool.Exec(ctx, `INSERT INTO session_managed_credential(session_id,account_id,engine,token_id,auth_session_id,runtime_token_id) SELECT $1,$2,$3,$4,$5,$6 FROM sessions WHERE id=$1 AND spawning_account_id=$2 AND engine=$3 AND runtime='cluster' AND ended_at IS NULL
 ON CONFLICT(session_id) DO UPDATE SET account_id=EXCLUDED.account_id,engine=EXCLUDED.engine,token_id=EXCLUDED.token_id,auth_session_id=EXCLUDED.auth_session_id,runtime_token_id=EXCLUDED.runtime_token_id`, spec.SessionID, spec.SpawningAccountID, spec.Engine, spec.TokenID, spec.AuthSessionID, token.ID)
	if err != nil || tag.RowsAffected() != 1 {
		return errManagedCredential
	}
	return nil
}
