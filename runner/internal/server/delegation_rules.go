package server

// Settings → Sessions starting sessions (spec §3): the person's standing rules ("always allow"),
// and the revoke of a session grant ("for this session"). A person's browser only, for their own
// rows; changes need a login with a sid, like a decision.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	msgRuleSignIn     = "sign in again to change this"
	msgRuleNotFound   = "not found"
	msgRuleBody       = `the body is {"engine", "runtime": "docker" | "daemon" | "cluster", "daemon_id" (not for "cluster"), "local_only"?}`
	maxRuleBodyBytes  = 4 << 10
	ruleCreatedViaSet = "settings"
)

type delegationRuleView struct {
	ID         string    `json:"id"`
	Engine     string    `json:"engine"`
	Runtime    string    `json:"runtime"`
	DaemonID   string    `json:"daemon_id"`
	DaemonName string    `json:"daemon_name"`
	LocalOnly  bool      `json:"local_only"`
	CreatedVia string    `json:"created_via"`
	CreatedAt  time.Time `json:"created_at"`
}

type delegationLimitsView struct {
	PerParentRunning        int `json:"per_parent_running"`
	PerParentStartsPerHour  int `json:"per_parent_starts_per_hour"`
	PerAccountRunning       int `json:"per_account_running"`
	PerAccountStartsPerHour int `json:"per_account_starts_per_hour"`
}

type delegationRulesReply struct {
	Rules  []delegationRuleView `json:"rules"`
	Limits delegationLimitsView `json:"limits"`
}

func ruleView(r db.DelegationRule) delegationRuleView {
	return delegationRuleView{ID: r.ID, Engine: r.Engine, Runtime: r.Runtime, DaemonID: r.DaemonID, DaemonName: r.DaemonName,
		LocalOnly: r.LocalOnly, CreatedVia: r.CreatedVia, CreatedAt: r.CreatedAt}
}

func (a *API) delegationLimits(ctx context.Context) delegationLimitsView {
	limits := a.effectiveChildLimits(ctx)
	return delegationLimitsView{PerParentRunning: limits.parallel, PerParentStartsPerHour: limits.hourly,
		PerAccountRunning: maxRunningChildrenPerAccount, PerAccountStartsPerHour: maxStartsPerAccountPerHour}
}

// ruleOwner is personAccount plus a login session (sid): the person changing their rules or grants.
func (a *API) ruleOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return "", false
	}
	if p.Kind != string(identity.Human) || p.OnBehalfOf != "" || p.Sub == "" {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	if p.Sid == "" {
		writeError(w, http.StatusUnauthorized, msgRuleSignIn)
		return "", false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return "", false
	}
	return p.Sub, true
}

// HandleGetDelegationRules is GET /api/delegation-rules: the caller's live rules and the limits.
func (a *API) HandleGetDelegationRules(w http.ResponseWriter, r *http.Request) {
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	rules, err := db.ListDelegationRules(r.Context(), a.dbPool, account)
	if err != nil {
		writeDatabaseFailure(w, "list the rules", "delegation ListDelegationRules", err)
		return
	}
	out := delegationRulesReply{Rules: make([]delegationRuleView, 0, len(rules)), Limits: a.delegationLimits(r.Context())}
	for _, rule := range rules {
		out.Rules = append(out.Rules, ruleView(rule))
	}
	writeJSON(w, http.StatusOK, out)
}

type postDelegationRuleBody struct {
	Engine    string `json:"engine"`
	Runtime   string `json:"runtime"`
	DaemonID  string `json:"daemon_id"`
	LocalOnly *bool  `json:"local_only"`
}

// HandlePostDelegationRule is POST /api/delegation-rules: a rule from Settings. local_only
// defaults on for Hermes and OpenClaw. The daemon must be a workstation the server knows (it may be
// offline: rules outlive a sleeping laptop); a cluster rule (runtime "cluster", on an install with a
// cluster runtime) names no daemon and is keyed on the cluster's, as a card's "Always allow … on the
// cluster" is. It applies to the cluster children of every direct session of the person, desktop or
// cluster.
func (a *API) HandlePostDelegationRule(w http.ResponseWriter, r *http.Request) {
	account, ok := a.ruleOwner(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRuleBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, msgRuleBody)
		return
	}
	var body postDelegationRuleBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusUnprocessableEntity, msgRuleBody)
		return
	}
	desc, known := engines.Lookup(strings.TrimSpace(body.Engine))
	if strings.TrimSpace(body.Engine) == "" || !known {
		writeError(w, http.StatusUnprocessableEntity, "unknown engine")
		return
	}
	if body.Runtime != runnerRuntimeDocker && body.Runtime != runnerRuntimeDaemon && body.Runtime != runnerRuntimeCluster {
		writeError(w, http.StatusUnprocessableEntity, childRuntimeMessageCluster)
		return
	}
	if !containsString(desc.Runtimes, body.Runtime) {
		writeError(w, http.StatusUnprocessableEntity, engineLabel(desc.ID)+" does not run in "+body.Runtime)
		return
	}
	switch {
	case body.Runtime == runnerRuntimeCluster:
		if a.hub.JobManager() == nil {
			writeError(w, http.StatusUnprocessableEntity, "this install has no cluster")
			return
		}
		if body.DaemonID != "" && body.DaemonID != clusterDaemonID {
			writeError(w, http.StatusUnprocessableEntity, "a cluster rule names no daemon")
			return
		}
		body.DaemonID = clusterDaemonID
		// The rule names the cluster's daemon row, which no cluster session may have made yet.
		if err := db.UpsertDaemon(r.Context(), a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
			writeDatabaseFailure(w, "check the daemon", "delegation cluster daemon", err)
			return
		}
	case !validSessionID(body.DaemonID) || body.DaemonID == clusterDaemonID:
		writeError(w, http.StatusUnprocessableEntity, "unknown daemon")
		return
	case !a.hub.ownsDesktopID(r.Context(), account, body.DaemonID):
		writeError(w, http.StatusUnprocessableEntity, "unknown daemon")
		return
	}

	localOnly := defaultLocalOnly(desc.ID)
	if body.LocalOnly != nil {
		localOnly = *body.LocalOnly
	}
	ctx := r.Context()
	var rule db.DelegationRule
	err = a.withDelegationLock(ctx, account, func() error {
		var err error
		rule, err = db.InsertDelegationRule(ctx, a.dbPool, db.DelegationRule{AccountID: account, Engine: desc.ID,
			Runtime: body.Runtime, DaemonID: body.DaemonID, LocalOnly: localOnly, CreatedVia: ruleCreatedViaSet})
		return err
	})
	if err != nil {
		writeDatabaseFailure(w, "add the rule", "delegation InsertDelegationRule", err)
		return
	}
	log.Printf("delegation: %s added rule %s", account, rule.ID)
	writeJSON(w, http.StatusCreated, ruleView(rule))
}

// HandleDeleteDelegationRule is DELETE /api/delegation-rules/{id}: revoke one of the caller's rules.
// Children it started keep running.
func (a *API) HandleDeleteDelegationRule(w http.ResponseWriter, r *http.Request) {
	account, ok := a.ruleOwner(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeError(w, http.StatusNotFound, msgRuleNotFound)
		return
	}
	ctx := r.Context()
	var done bool
	err := a.withDelegationLock(ctx, account, func() error {
		var err error
		done, err = db.RevokeDelegationRule(ctx, a.dbPool, id, account)
		return err
	})
	if err != nil {
		writeDatabaseFailure(w, "remove the rule", "delegation RevokeDelegationRule", err)
		return
	}
	if !done {
		writeError(w, http.StatusNotFound, msgRuleNotFound)
		return
	}
	log.Printf("delegation: %s removed rule %s", account, id)
	w.WriteHeader(http.StatusNoContent)
}

// HandleRevokeDelegationGrant is POST /api/delegation-grants/{id}/revoke: end one of the caller's
// session grants. Children it started keep running.
func (a *API) HandleRevokeDelegationGrant(w http.ResponseWriter, r *http.Request) {
	account, ok := a.ruleOwner(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeError(w, http.StatusNotFound, msgRuleNotFound)
		return
	}
	done, err := a.revokeGrantFenced(r.Context(), id, account)
	if err != nil {
		writeDatabaseFailure(w, "revoke the grant", "delegation RevokeDelegationGrant", err)
		return
	}
	if !done {
		writeError(w, http.StatusNotFound, msgRuleNotFound)
		return
	}
	log.Printf("delegation: %s revoked grant %s", account, id)
	w.WriteHeader(http.StatusNoContent)
}
