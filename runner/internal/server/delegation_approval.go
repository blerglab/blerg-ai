package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

type childApprovalRuntime struct {
	Runtime                 string   `json:"runtime"`
	Enabled                 bool     `json:"enabled"`
	Partial                 bool     `json:"partial"`
	AllowedEngines          []string `json:"allowed_engines"`
	GrantedEngines          []string `json:"granted_engines"`
	StandingRuleEngines     []string `json:"standing_rule_engines"`
	EffectiveAllowedEngines []string `json:"effective_allowed_engines"`
	CanEnable               bool     `json:"can_enable"`
	Reason                  string   `json:"reason"`
}
type childApprovalView struct {
	Revision       string                 `json:"revision"`
	DaemonID       string                 `json:"daemon_id"`
	AllowedEngines []string               `json:"allowed_engines"`
	Runtimes       []childApprovalRuntime `json:"runtimes"`
}
type childApprovalWrite struct {
	Runtime  string `json:"runtime"`
	Enabled  *bool  `json:"enabled"`
	Revision string `json:"revision"`
}

// childApprovalSnapshot runs under the account lock and parent row fence. Grants and rules are
// exact scopes; discovery never expands an existing grant to a newly installed engine.
// local is clusterLocalEngines for a parent with a cluster row (a cluster parent, or a desktop
// parent on an install with a cluster), computed by the caller before it takes the account and
// parent-row locks (it may ask core).
//
// A cluster parent has one row (cluster). A desktop parent has its desktop's two (docker, daemon)
// and, when the install has a cluster runtime, a third (cluster), keyed on the cluster's daemon:
// each row has its own engines and its own reason, so a sleeping desktop does not hide the cluster
// row, nor a cluster still being checked the desktop rows.
func (a *API) childApprovalSnapshot(ctx context.Context, tx pgx.Tx, parent *db.SessionRow, local []string) (childApprovalView, []db.DelegationGrant, error) {
	view := childApprovalView{DaemonID: delegationDaemonID(parent), AllowedEngines: []string{}, Runtimes: []childApprovalRuntime{}}
	grants, err := db.ListLiveDelegationGrants(ctx, tx, parent.ID)
	if err != nil {
		return view, nil, err
	}
	restricted := parent.RestrictTools
	cluster := onClusterParent(parent)
	jm := a.hub.JobManager()
	clusterRow := cluster || onDesktopDaemon(parent) && jm != nil
	var dc *DaemonConn
	if !cluster {
		dc = a.hub.GetHostDaemon(parent.DaemonID)
	}
	// Cluster children's engines are the readiness snapshot's.
	var clusterEngines []string
	clusterKnown := false
	if clusterRow && jm != nil {
		clusterEngines, clusterKnown = a.clusterAvailableEngines(ctx, a.delegateFor(ctx, parent, runnerRuntimeCluster, ""))
	}
	reason := ""
	switch {
	case parent.DiscardRequestedAt != nil || db.TerminallyEnded(parent) || terminalSessionStatus(parent.Status):
		reason = "This parent session is not active."
	case !trustDirect(parent) || restricted:
		reason = "Only a direct, unrestricted personal session can have session approval."
	}
	clusterReason := ""
	switch {
	case jm == nil:
		clusterReason = "The cluster runtime is not configured."
	case !clusterKnown:
		clusterReason = "The cluster's engine credentials are still being checked. Reload in a moment."
	}
	desktopReason := ""
	switch {
	case !onDesktopDaemon(parent):
		desktopReason = "Child sessions require the parent's desktop daemon."
	case dc == nil:
		desktopReason = "The parent's desktop daemon is disconnected."
	case !dc.RecoveryReadyCapable:
		desktopReason = "Update the daemon before enabling session approval."
	case dc.AvailableEngines() == nil:
		desktopReason = "The daemon has not reported its available engines."
	}
	runtimes := []string{}
	if !cluster {
		runtimes = append(runtimes, runnerRuntimeDocker, runnerRuntimeDaemon)
	}
	if clusterRow {
		runtimes = append(runtimes, runnerRuntimeCluster)
	}
	host := childHost{dc: dc}
	clusterHost := childHost{cluster: true, local: local}
	allRuleIDs := []string{}
	for _, runtime := range runtimes {
		onCluster := runtime == runnerRuntimeCluster
		daemonID := childDaemonID(parent, runtime)
		rowHost, rowReason := host, desktopReason
		if onCluster {
			rowHost, rowReason = clusterHost, clusterReason
		}
		state := childApprovalRuntime{Runtime: runtime, AllowedEngines: []string{}, GrantedEngines: []string{}, StandingRuleEngines: []string{}, EffectiveAllowedEngines: []string{}, Reason: reason}
		if state.Reason == "" {
			state.Reason = rowReason
		}
		if state.Reason == "" && !confinementOK(parent, runtime) {
			state.Reason = confinementReason(runtime)
		}
		for _, descriptor := range engines.All() {
			if onCluster {
				if !slices.Contains(clusterEngines, descriptor.ID) {
					continue
				}
			} else if dc == nil || !slices.Contains(dc.AvailableEngines(), descriptor.ID) || !descriptor.SupportsRuntime(runtime) || hostEngineState(dc, HostSelection{Kind: "agent", Engine: descriptor.ID, Runtime: runtime}) != "available" {
				continue
			}
			state.AllowedEngines = append(state.AllowedEngines, descriptor.ID)
			if !slices.Contains(view.AllowedEngines, descriptor.ID) {
				view.AllowedEngines = append(view.AllowedEngines, descriptor.ID)
			}
			var rules []db.DelegationRule
			if standingRulesApply(parent, runtime) {
				rules, err = db.CandidateDelegationRules(ctx, tx, derefOrEmpty(parent.SpawningAccountID), descriptor.ID, runtime, daemonID)
				if err != nil {
					return view, nil, err
				}
			}
			for _, rule := range rules {
				allRuleIDs = append(allRuleIDs, rule.ID)
				if trustDirect(parent) && !restricted && confinementOK(parent, runtime) && (!rule.LocalOnly || rowHost.localEngine(descriptor.ID)) {
					state.StandingRuleEngines = append(state.StandingRuleEngines, descriptor.ID)
					break
				}
			}
		}
		for _, grant := range grants {
			if grant.DaemonID == daemonID && grant.Runtime == runtime {
				state.GrantedEngines = append(state.GrantedEngines, grant.Engine)
			}
		}
		slices.Sort(state.GrantedEngines)
		matched := 0
		for _, engine := range state.AllowedEngines {
			granted := slices.Contains(state.GrantedEngines, engine)
			if granted {
				matched++
			}
			if state.Reason == "" && (granted || slices.Contains(state.StandingRuleEngines, engine)) {
				state.EffectiveAllowedEngines = append(state.EffectiveAllowedEngines, engine)
			}
		}
		if state.Reason == "" && len(state.AllowedEngines) == 0 {
			state.Reason = "No currently available engine supports this runtime."
		}
		state.CanEnable = state.Reason == ""
		state.Enabled = state.CanEnable && matched == len(state.AllowedEngines)
		state.Partial = len(state.GrantedEngines) > 0 && !state.Enabled
		view.Runtimes = append(view.Runtimes, state)
	}
	slices.Sort(view.AllowedEngines)
	slices.Sort(allRuleIDs)
	grantIDs := []string{}
	for _, g := range grants {
		grantIDs = append(grantIDs, g.ID)
	}
	slices.Sort(grantIDs)
	raw, _ := json.Marshal(struct {
		Parent, Owner string
		Epoch, Ended  *time.Time
		View          childApprovalView
		Grants, Rules []string
	}{parent.ID, derefOrEmpty(parent.SpawningAccountID), parent.DelegationEndedAt, parent.EndedAt, view, grantIDs, allRuleIDs})
	digest := sha256.Sum256(raw)
	view.Revision = hex.EncodeToString(digest[:])
	return view, grants, nil
}

// Both reads and writes require the human owner's current browser login. A session/runtime or
// delegated agent credential can neither inspect these controls nor change future authority.
func (a *API) HandleChildApproval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := a.ruleOwner(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body childApprovalWrite
	if r.Method == http.MethodPut {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid session approval body")
			return
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Enabled == nil || (body.Runtime != runnerRuntimeDocker && body.Runtime != runnerRuntimeDaemon && body.Runtime != runnerRuntimeCluster) || len(body.Revision) != 64 {
			writeError(w, http.StatusUnprocessableEntity, "the body requires runtime, enabled, and the current revision")
			return
		}
	}
	view, problem, err := a.applyChildApproval(ctx, account, sid, r.Method == http.MethodPut, body, true, "session_settings")
	if errors.Is(err, errDelegationBusy) {
		writeError(w, http.StatusServiceUnavailable, msgDelegationBusy)
		return
	}
	if err != nil {
		writeDatabaseFailure(w, "change session approval", "child approval", err)
		return
	}
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// msgChildRuntimeUnavailable answers a change for a runtime this parent's children never run on
// (sandboxed children under a cluster parent, or cluster children on an install with no cluster).
const msgChildRuntimeUnavailable = "This session cannot start children on that runtime."

// applyChildApproval reads (write false) or changes a parent's child approval under the account's
// delegation lock and the parent row fence: the one implementation behind the session panel's
// toggles and the launch sheet's pre-authorization. checkRevision false (the launch sheet, for a
// session that just started) takes the current snapshot as the one being changed. source names
// the change in its audit event.
func (a *API) applyChildApproval(ctx context.Context, account, sid string, write bool, body childApprovalWrite, checkRevision bool, source string) (childApprovalView, *APIError, error) {
	// The cluster local-hardware answer may ask core: before the locks, from an unlocked read
	// (the locked snapshot below re-checks everything else).
	var clusterLocal []string
	if pre, err := db.GetSession(ctx, a.dbPool, sid); err == nil && pre != nil && ownsSession(account, pre) && (onClusterParent(pre) || onDesktopDaemon(pre)) && a.hub.JobManager() != nil {
		clusterLocal = a.clusterLocalEngines(ctx, pre, a.delegateFor(ctx, pre, runnerRuntimeCluster, ""))
	}
	var view childApprovalView
	var problem *APIError
	var eventID string
	var eventPayload json.RawMessage
	var eventSeq int64
	var eventAt time.Time
	err := a.withDelegationLock(ctx, account, func() error {
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		parent, err := db.LockDelegationParent(ctx, tx, sid)
		if err != nil {
			return err
		}
		if !ownsSession(account, parent) {
			problem = apiErrorf(http.StatusNotFound, "not found")
			return nil
		}
		var grants []db.DelegationGrant
		view, grants, err = a.childApprovalSnapshot(ctx, tx, parent, clusterLocal)
		if err != nil {
			return err
		}
		if !write {
			return nil
		}
		if checkRevision && body.Revision != view.Revision {
			problem = apiErrorf(http.StatusConflict, "Session approval changed. Reload before changing it.")
			return nil
		}
		var selected childApprovalRuntime
		for _, state := range view.Runtimes {
			if state.Runtime == body.Runtime {
				selected = state
			}
		}
		if selected.Runtime == "" { // a runtime this parent's children never run on
			problem = apiErrorf(http.StatusUnprocessableEntity, "%s", msgChildRuntimeUnavailable)
			return nil
		}
		if *body.Enabled && !selected.CanEnable {
			problem = apiErrorf(http.StatusConflict, "%s", selected.Reason)
			return nil
		}
		changed := []string{}
		daemonID := childDaemonID(parent, body.Runtime)
		if *body.Enabled {
			for _, engine := range selected.AllowedEngines {
				found := false
				for _, g := range grants {
					if g.Engine == engine && g.Runtime == body.Runtime && g.DaemonID == daemonID {
						found = true
						break
					}
				}
				if found {
					continue
				}
				grant, err := db.InsertDelegationGrantTx(ctx, tx, db.DelegationGrant{ParentSessionID: sid, AccountID: account, Engine: engine, Runtime: body.Runtime, DaemonID: daemonID})
				if err != nil {
					return err
				}
				changed = append(changed, grant.ID)
			}
		} else {
			changed, err = db.RevokeChildApprovalTx(ctx, tx, sid, account, daemonID, body.Runtime)
			if err != nil {
				return err
			}
		}
		if len(changed) > 0 {
			slices.Sort(changed)
			eventID = newUUID()
			eventAt = time.Now()
			eventPayload, _ = json.Marshal(map[string]any{"runtime": body.Runtime, "enabled": *body.Enabled, "daemon_id": daemonID, "grant_ids": changed, "source": source})
			eventSeq, _, err = db.AppendAgentEventTx(ctx, tx, sid, eventID, "delegation_permissions_changed", string(eventPayload), eventAt)
			if err != nil {
				return err
			}
		}
		view, _, err = a.childApprovalSnapshot(ctx, tx, parent, clusterLocal)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil || problem != nil {
		return view, problem, err
	}
	// Session-visible audit records expose opaque grant IDs, never credentials or the
	// owner account. Public child metadata already names the same IDs in delegation_via.
	// Private parent events remain protected by the normal subscription privacy fence.
	if eventSeq > 0 {
		fanOutServerEvent(a.hub, sid, eventID, "delegation_permissions_changed", eventPayload, eventSeq, eventAt)
	}
	return view, nil, nil
}

// Legacy one-grant revocation shares admission with proactive changes and starts, so an old
// revision cannot recreate a grant after the owner has successfully revoked it.
func (a *API) revokeGrantFenced(ctx context.Context, id, account string) (bool, error) {
	done := false
	err := a.withDelegationLock(ctx, account, func() error {
		grant, err := db.GetOwnedDelegationGrant(ctx, a.dbPool, id, account)
		if err != nil || grant == nil {
			return err
		}
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		parent, err := db.LockDelegationParent(ctx, tx, grant.ParentSessionID)
		if err != nil {
			return err
		}
		if !ownsSession(account, parent) {
			return nil
		}
		done, err = db.RevokeDelegationGrant(ctx, tx, id, account)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return done, err
}
