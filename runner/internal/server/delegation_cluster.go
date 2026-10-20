package server

// Child sessions on the cluster (0.4.3). A parent running in a cluster pod starts children that run
// as cluster pods: the same allowlisted start, rules, grants, cards, limits, child routes and
// cascades as a desktop parent's children (delegation*.go), with these differences:
//
//   - the child's runtime is "cluster", the default for a cluster parent; a cluster parent cannot
//     start a desktop child, nor a desktop parent a cluster child;
//   - its repo resolves the way a cluster launch's does (cloned into the pod), not from a daemon's
//     checked-out folders;
//   - engines are the cluster's (descriptor runtimes and the cluster readiness snapshot);
//   - rules and grants name runtime "cluster" on the cluster's synthetic daemon, so a desktop rule
//     never starts a cluster child, nor the reverse;
//   - the child pod gets the owner's credentials like any cluster session of the owner, against a
//     liveness proof: the browser session of the person who just allowed the start, else the
//     parent's recorded launch proof (clusterOwnerProof). With no proof at all, a rule or grant does
//     not start the child: the person is asked, and their decision carries one.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// clusterDaemonLabel is the cluster's name where a desktop child's names its daemon: the card's
// "Always allow …" target and the budget's daemon.
const clusterDaemonLabel = "the cluster"

// msgClusterChildNoProof: the owner's credentials cannot be released to the child pod.
const msgClusterChildNoProof = "the owner's sign-in cannot authorize a child on the cluster; ask the person to allow it from the card"

// childHost is where a parent's children run: its desktop daemon (dc, nil while disconnected) or,
// for a cluster parent, the cluster.
type childHost struct {
	dc      *DaemonConn
	cluster bool
	// ownerLive (a cluster host only): core accepts the delegate's proof for the owner's
	// credentials right now (clusterOwnerLive). A rule or grant starts a cluster child only then.
	ownerLive bool
	// local (a cluster host only): the engines a child of this owner runs on local hardware
	// (clusterLocalEngines), which a local_only rule needs.
	local []string
}

func hostOf(parent *db.SessionRow, dc *DaemonConn) childHost {
	return childHost{dc: dc, cluster: onClusterParent(parent)}
}

// name is what the card and the budget call the host.
func (h childHost) name() string {
	if h.cluster {
		return clusterDaemonLabel
	}
	if h.dc != nil {
		return h.dc.Name
	}
	return ""
}

// localEngine: a rule with local_only matches only an engine the host runs locally. On the
// cluster that is Hermes on the shared providers' local model servers, by the desktop's rule
// (clusterLocalEngines); every other engine calls a provider.
func (h childHost) localEngine(engine string) bool {
	if h.cluster {
		return slices.Contains(h.local, engine)
	}
	return h.dc.LocalEngine(engine)
}

// recordLaunchAuthSession records the owner's browser session behind a cluster launch or resume
// (best effort, logged; "" records nothing). Its only callers are cluster paths (HandlePostSessions'
// cluster branch, startClusterNoRepo, startRunnerSession's cluster half, the cluster resume), so a
// desktop session's column stays NULL, as migration 066 says.
func recordLaunchAuthSession(ctx context.Context, pool *pgxpool.Pool, sessionID, sid string) {
	if pool == nil || sid == "" {
		return
	}
	if err := db.SetSessionLaunchAuthSession(ctx, pool, sessionID, sid); err != nil {
		log.Printf("record launch proof %s: %v", sessionID, err)
	}
}

// clusterOwnerProof is the proof a cluster parent's child is started (or resumed) with: decidedSid
// when the person just allowed it, else the parent's recorded browser session, else the agent token
// it was launched with. The zero proof when there is none (or no database).
func (a *API) clusterOwnerProof(ctx context.Context, parentID, decidedSid string) coreProof {
	if decidedSid != "" {
		return coreProof{SessionID: decidedSid}
	}
	if a.dbPool == nil {
		return coreProof{}
	}
	tokenID, sid, err := db.GetSessionLaunchProof(ctx, a.dbPool, parentID)
	if err != nil {
		log.Printf("delegation: launch proof of %s: %v", parentID, err)
		return coreProof{}
	}
	if sid != "" {
		return coreProof{SessionID: sid}
	}
	if tokenID != "" {
		return coreProof{TokenID: tokenID}
	}
	return coreProof{}
}

// clusterOwnerLive reports whether a child pod could get the owner's credentials with p's proof
// now: core is not wired for personal credentials (the operator Secret serves), or core accepts the
// proof (verifyCorePrincipal: live and the owner's). A missing, expired or signed-out proof, or a
// core that cannot answer, is not live: a rule or grant then asks the person instead of starting a
// child that would fail at its credential fetch.
func (a *API) clusterOwnerLive(ctx context.Context, p runnerPrincipal) bool {
	jm := a.hub.JobManager()
	if jm == nil {
		return false
	}
	if !jm.personalCredentialsWired() {
		return true
	}
	if !p.proof().valid() {
		return false
	}
	return verifyCorePrincipal(ctx, jm.CredentialClient, jm.CoreURL, jm.CoreInternalKey, p.spawningAccountID(), p.proof()) == nil
}

// clusterEngineProblem checks an engine against the cluster as a launch does: the engine runs in a
// cluster pod, and the cluster readiness snapshot (operator Secret plus the owner's personal
// credentials), once known, lists it. An unknown snapshot does not refuse: the Job's own credential
// lookup fails closed.
func (a *API) clusterEngineProblem(ctx context.Context, engine string, p runnerPrincipal) string {
	if d, ok := engines.Lookup(engine); !ok || !d.SupportsRuntime(runnerRuntimeCluster) {
		return "the cluster cannot run " + engineLabel(engine)
	}
	available, known, _ := a.clusterReadinessSnapshot(ctx, p, false)
	if known && !slices.Contains(available, engine) {
		return "the cluster has no credentials for " + engineLabel(engine)
	}
	return ""
}

// clusterAvailableEngines is the cluster readiness snapshot for p: the engines a cluster child may
// run, and whether the snapshot is known yet.
func (a *API) clusterAvailableEngines(ctx context.Context, p runnerPrincipal) ([]string, bool) {
	available, known, _ := a.clusterReadinessSnapshot(ctx, p, false)
	out := []string{}
	for _, d := range engines.All() {
		if d.SupportsRuntime(runnerRuntimeCluster) && slices.Contains(available, d.ID) {
			out = append(out, d.ID)
		}
	}
	return out, known
}

// budgetForCluster is a cluster parent's budget: per engine, whether the cluster can run it and the
// usage windows the parent's own pod reports for its provider (children run on the same account's
// credentials). pod is that pod's daemon (nil while it is away).
func budgetForCluster(pod *DaemonConn, available []string, known bool, local []string, now time.Time) budgetReply {
	out := budgetReply{Daemon: clusterDaemonLabel, Engines: make([]budgetEngine, 0, len(budgetEngines)), FetchedAt: now.UTC()}
	for _, e := range budgetEngines {
		b := budgetEngine{Engine: e.engine, Windows: []budgetWindow{}}
		d, ok := engines.Lookup(e.engine)
		b.Available = ok && d.SupportsRuntime(runnerRuntimeCluster) && (!known || slices.Contains(available, e.engine))
		if e.provider == "" {
			if slices.Contains(local, e.engine) {
				b.Local, b.Note = true, budgetLocalNote
			} else {
				b.Note = budgetRemoteNote
			}
			out.Engines = append(out.Engines, b)
			continue
		}
		if pod != nil {
			for _, u := range pod.UsageLimits() {
				if u.Provider != e.provider {
					continue
				}
				for _, w := range u.Windows {
					b.Windows = append(b.Windows, budgetWindow{ID: w.ID, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt})
					if w.ID == "capped" && !w.ResetsAt.IsZero() && w.ResetsAt.After(now) {
						at := w.ResetsAt
						b.CappedUntil = &at
					}
				}
			}
		}
		out.Engines = append(out.Engines, b)
	}
	return out
}

// budgetEngineForHost is one engine's budget line for the card and the start reply.
func (a *API) budgetEngineForHost(ctx context.Context, parent *db.SessionRow, host childHost, p runnerPrincipal, engine string, now time.Time) *budgetEngine {
	if !host.cluster {
		return budgetEngineFor(host.dc, engine, now)
	}
	available, known := a.clusterAvailableEngines(ctx, p)
	reply := budgetForCluster(a.hub.FindDaemonForSession(parent.ID), available, known, host.local, now)
	for i := range reply.Engines {
		if reply.Engines[i].Engine == engine {
			return &reply.Engines[i]
		}
	}
	return nil
}

// startClusterChild is the cluster driver of a delegate's start (startRunnerSession): the child
// row goes in with its parent link and the parent's epoch check, private when the parent is, marked
// as started by a session; the pod gets its own messaging token, the owner's credentials against
// the delegate's proof, no plugins, and must report recovery-ready like a desktop child. Every
// refusal comes before the Job exists; a child row that could not be fully recorded is removed.
func (a *API) startClusterChild(ctx context.Context, req runnerStartRequest, principal runnerPrincipal, sessionID string) *APIError {
	jm := a.hub.JobManager()
	if jm == nil {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgClusterNotConfigured)
	}
	if a.dbPool == nil {
		return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "the child session could not be recorded — retry")
	}
	parent, err := db.GetSession(ctx, a.dbPool, req.ParentSessionID)
	if err != nil {
		return apiErrorf(http.StatusInternalServerError, "could not read the parent session")
	}
	if parent == nil {
		return apiErrorf(http.StatusNotFound, "session not found")
	}
	if !onClusterParent(parent) {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgChildrenOnDaemon)
	}
	account := principal.spawningAccountID()
	proof := principal.proof()
	if account == "" || account != derefOrEmpty(parent.SpawningAccountID) {
		return apiErrorf(http.StatusForbidden, "%s", msgDelegateNoOwner)
	}
	if jm.personalCredentialsWired() && !proof.valid() {
		return apiErrorf(http.StatusConflict, "%s", msgClusterChildNoProof)
	}
	gitURL, workdir := "", clusterNoRepoWorkdir
	if !req.NoRepo {
		gitURL = jm.CloneURLFor("", req.Repo)
		workdir = "/workspace/" + req.Repo
	}
	if err := db.UpsertDaemon(ctx, a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		log.Printf("cluster child: cluster daemon upsert: %v", err)
	}
	origin := sessionOriginFor(account, parent.Private, "")
	origin.ParentSessionID, origin.DelegationVia, origin.KeepRunning = req.ParentSessionID, req.DelegationVia, req.KeepRunning
	origin.RequireParentEpoch, origin.ParentEpoch = true, req.ParentEpoch
	a.notePrivateInsert(sessionID, origin)
	if err := db.InsertClusterSessionAs(ctx, a.dbPool, sessionID, clusterDaemonID, "starting", workdir, req.Repo, req.Title, req.Model, origin); err != nil {
		if errors.Is(err, db.ErrDelegationEnded) {
			return apiErrorf(http.StatusConflict, "%s", msgDelegateEnded)
		}
		log.Printf("cluster child %s: insert: %v", sessionID, err)
		return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "the child session could not be recorded — retry")
	}
	abort := func() *APIError {
		abortSpawnSessionToken(ctx, a.dbPool, sessionID)
		return apiErrorCause(http.StatusServiceUnavailable, errDaemonUnavailable, "the child session could not be recorded — retry")
	}
	// Must land: it is one of the marks that keep a child a child (isChildSession).
	if err := db.SetSessionStartedBy(ctx, a.dbPool, sessionID, "session", a.parentLabel(ctx, parent.ID)); err != nil {
		log.Printf("cluster child %s: started_by: %v", sessionID, err)
		return abort()
	}
	// A cron's child never runs on operator-supplied material, on this start or any resume: the
	// mark must land before the Job does.
	cronChild := derefOrEmpty(parent.CronID) != "" || parent.StartedByKind == "cron"
	if cronChild {
		if err := db.SetSessionNoOperatorFallback(ctx, a.dbPool, sessionID); err != nil {
			log.Printf("cluster child %s: no_operator_fallback: %v", sessionID, err)
			return abort()
		}
	}
	if err := db.SetSessionSpawningAccount(ctx, a.dbPool, sessionID, account); err != nil {
		log.Printf("cluster child %s: SetSessionSpawningAccount: %v", sessionID, err)
	}
	if err := db.SetSessionKind(ctx, a.dbPool, sessionID, "agent"); err != nil {
		log.Printf("cluster child %s: SetSessionKind: %v", sessionID, err)
	}
	recordLaunchEffort(ctx, a.dbPool, sessionID, req.Effort)
	if err := db.SetSessionEngine(ctx, a.dbPool, sessionID, req.Engine); err != nil {
		log.Printf("cluster child %s: SetSessionEngine: %v", sessionID, err)
	}
	if gitURL != "" {
		if err := db.SetSessionGitURL(ctx, a.dbPool, sessionID, gitURL); err != nil {
			log.Printf("cluster child %s: SetSessionGitURL: %v", sessionID, err)
		}
	}
	recordInteraction(ctx, a.dbPool, sessionID, req.Interaction)
	// The child's own messaging token (cap "message"), scoped as a desktop child's: the
	// `blerg-runner` CLI in its pod cannot work without it, so a start that could not mint it is
	// refused rather than started mute.
	env := withClusterSessionToken(ctx, a.dbPool, sessionID, nil)
	if env[sessionTokenEnv] == "" {
		return abort()
	}
	spec := SessionJobSpec{
		// A cron's session never runs on operator-supplied material (credentials, or the shared
		// Hermes providers); neither does a child it starts.
		NoOperatorFallback:   cronChild,
		RequireRecoveryReady: true,
		Interaction:          req.Interaction,
		SessionID:            sessionID, Repo: req.Repo, Title: req.Title,
		Model: req.Model, Effort: req.Effort, Engine: req.Engine, InitialPrompt: req.Prompt,
		InitialPromptSource: promptSourceSession, // the parent wrote it, not the person
		ExtraEnv:            env,
		GitURL:              gitURL,
		NoRepo:              req.NoRepo,
		SpawningAccountID:   account,
		TokenID:             proof.TokenID,
		AuthSessionID:       proof.SessionID,
		// A child loads none of the owner's plugins, as on a desktop (pluginsNoteDelegated).
		PluginsResolved: true,
		PluginsNote:     pluginsNoteDelegated,
	}
	announceClusterStart(ctx, a.hub, a.dbPool, sessionID, req.Repo, false, pluginStage(nil, pluginsNoteDelegated))
	if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		a.markClusterStartFailed(ctx, sessionID, err.Error())
		// No pod will ever use the child's messaging token.
		if rerr := db.RevokeBoardTokensForSession(ctx, a.dbPool, sessionID); rerr != nil {
			log.Printf("cluster child %s: revoke token after a failed start: %v", sessionID, rerr)
		}
		return apiErrorCause(http.StatusServiceUnavailable, startFailureCause(err), "%s", err.Error())
	}
	clusterJobCreated(ctx, a.hub, a.dbPool, sessionID, a.startWatchEvery())
	return nil
}
