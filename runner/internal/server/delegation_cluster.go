package server

// Child sessions on the cluster (0.4.3, 0.4.4). A parent running in a cluster pod starts children
// that run as cluster pods; so does a desktop parent that asks for runtime "cluster" on an install
// with a cluster runtime. The same allowlisted start, rules, grants, cards, limits, child routes and
// cascades as a desktop parent's desktop children (delegation*.go), with these differences:
//
//   - a cluster parent's children run on the cluster (runtime "cluster", its default); a desktop
//     parent's run on its desktop unless it asks for "cluster" (never its default); a cluster parent
//     cannot start a desktop child;
//   - its repo resolves the way a cluster launch's does (cloned into the pod), not from a daemon's
//     checked-out folders; for a desktop parent a one-segment folder name is refused unless the
//     cluster resolves bare names (an org or an explicit git base), as a launch refuses it;
//   - engines are the cluster's (descriptor runtimes and the cluster readiness snapshot);
//   - rules and grants name runtime "cluster" on the cluster's synthetic daemon, so a desktop rule
//     never starts a cluster child, nor the reverse; a desktop parent has both sets;
//   - the child pod gets the owner's credentials like any cluster session of the owner, against a
//     liveness proof: the browser session of the person who just allowed the start, else the
//     parent's recorded launch proof (clusterOwnerProof). With no live proof, a rule or grant does
//     not start the child: the person is asked, and their decision carries one. The proof a child
//     was started with is recorded on the child (launch_auth_session_id, or token_id for an agent
//     token) and a desktop parent resumes its idle cluster child with it (delegateChild).
//
// No proof, token or credential ever goes to a desktop daemon or to the parent agent: the proof is
// a server-side value core checks, and the credentials go only into the child pod's Secret.

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

// msgNoClusterRuntime: a desktop parent asked for a cluster child on an install with no cluster.
const msgNoClusterRuntime = `this install has no cluster: a desktop session's children run on its desktop (runtime "docker" or "daemon")`

// msgClusterChildBareRepo: a desktop folder name the cluster cannot clone (no org or git base).
const msgClusterChildBareRepo = "the cluster could not resolve this desktop folder; use owner/name or make its git remote available on the parent desktop"

// msgClusterChildResumeNoProof: an idle cluster child's resume has no live owner sign-in.
const msgClusterChildResumeNoProof = "the sign-in this child was started with is no longer live, so it cannot resume on the cluster; ask the person to open the child and send the message from their browser"

// childHost is where a parent's child runs: its desktop daemon (dc, nil while disconnected) or the
// cluster (a cluster parent's children, and a desktop parent's runtime "cluster" child).
type childHost struct {
	repoSource *DaemonConn
	dc         *DaemonConn
	cluster    bool
	// clusterOn (a desktop parent's host only): the install has a cluster runtime, so a child may
	// ask for runtime "cluster" (forRuntime).
	clusterOn bool
	// fromDesktop (a cluster host only): the child of a desktop parent.
	fromDesktop bool
	// bareRepo (a cluster host only): a one-segment repo name resolves on the cluster. Always for a
	// cluster parent (as before 0.4.4); for a desktop parent, the fallback for an unknown origin
	// needs an org or an explicit git base, as a cluster launch requires.
	bareRepo bool
	// ownerLive (a cluster host only): core accepts the delegate's proof for the owner's
	// credentials right now (clusterOwnerLive). A rule or grant starts a cluster child only then.
	ownerLive bool
	// local (a cluster host only): the engines a child of this owner runs on local hardware
	// (clusterLocalEngines), which a local_only rule needs.
	local []string
}

// parentHost is where parent's children run by default: the cluster for a cluster parent, else its
// desktop daemon (dc), noting whether the install also has a cluster for a runtime "cluster" child.
func (a *API) parentHost(parent *db.SessionRow, dc *DaemonConn) childHost {
	h := childHost{dc: dc, cluster: onClusterParent(parent), bareRepo: true}
	if !h.cluster && onDesktopDaemon(parent) {
		if jm := a.hub.JobManager(); jm != nil {
			h.clusterOn = true
			h.bareRepo = jm.GitHubOrg != "" || jm.ExplicitGitURLBase != ""
		}
	}
	return h
}

// forRuntime is the host of a child on runtime: the cluster for a desktop parent's runtime
// "cluster" child when the install has one, else h itself.
func (h childHost) forRuntime(runtime string) childHost {
	if !h.cluster && h.clusterOn && runtime == runnerRuntimeCluster {
		return childHost{cluster: true, fromDesktop: true, bareRepo: h.bareRepo, repoSource: h.dc}
	}
	return h
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

// recordLaunchAuthSession records the owner's browser session behind a launch or resume (best
// effort, logged; "" records nothing): a cluster launch or resume, a desktop launch from the browser
// or by a person's token (0.4.4: its cluster children are authorized with it), a decision on a
// child's card (the parent's newest proof), and the proof a cluster child was started with.
func recordLaunchAuthSession(ctx context.Context, pool *pgxpool.Pool, sessionID, sid string) {
	if pool == nil || sid == "" {
		return
	}
	if err := db.SetSessionLaunchAuthSession(ctx, pool, sessionID, sid); err != nil {
		log.Printf("record launch proof %s: %v", sessionID, err)
	}
}

// clusterOwnerProof is the proof a cluster child is started (or a cluster parent's child resumed)
// with: decidedSid when the person just allowed it, else the parent's recorded browser session, else
// the agent token it was launched with. The zero proof when there is none (or no database).
func (a *API) clusterOwnerProof(ctx context.Context, parentID, decidedSid string) coreProof {
	if decidedSid != "" {
		return coreProof{SessionID: decidedSid}
	}
	return a.sessionLaunchProof(ctx, parentID)
}

// sessionLaunchProof is the proof recorded on a session: its browser session, else its agent token;
// the zero proof when there is none (or no database). For a cluster child it is the proof the child
// was started with (startClusterChild), which a desktop parent resumes the child with.
func (a *API) sessionLaunchProof(ctx context.Context, sessionID string) coreProof {
	if a.dbPool == nil {
		return coreProof{}
	}
	tokenID, sid, err := db.GetSessionLaunchProof(ctx, a.dbPool, sessionID)
	if err != nil {
		log.Printf("delegation: launch proof of %s: %v", sessionID, err)
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
	live, _ := a.clusterOwnerState(ctx, p)
	return live
}

// clusterOwnerState is clusterOwnerLive, also telling whether the answer is known: known false when
// core could not be asked or answered with an error (a retry may succeed), true when it accepted or
// refused the proof, or none is needed.
func (a *API) clusterOwnerState(ctx context.Context, p runnerPrincipal) (live, known bool) {
	jm := a.hub.JobManager()
	if jm == nil {
		return false, true
	}
	if !jm.personalCredentialsWired() {
		return true, true
	}
	if !p.proof().valid() {
		return false, true
	}
	err := verifyCorePrincipal(ctx, jm.CredentialClient, jm.CoreURL, jm.CoreInternalKey, p.spawningAccountID(), p.proof())
	if err == nil {
		return true, true
	}
	return false, errors.Is(err, errCorePrincipalRefused)
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
	reply := budgetForCluster(clusterParentPod(a.hub, parent), available, known, host.local, now)
	for i := range reply.Engines {
		if reply.Engines[i].Engine == engine {
			return &reply.Engines[i]
		}
	}
	return nil
}

// clusterParentPod is a cluster parent's own pod connection (nil while it is away, and for a desktop
// parent: a desktop's usage windows and local engines say nothing about the cluster).
func clusterParentPod(h *Hub, parent *db.SessionRow) *DaemonConn {
	if !onClusterParent(parent) {
		return nil
	}
	return h.FindDaemonForSession(parent.ID)
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
	if !onClusterParent(parent) && !onDesktopDaemon(parent) {
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
		gitURL = jm.CloneURLFor(req.Provider, req.Repo)
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
	// The proof the child starts with is its own from now on: a desktop parent resumes the idle
	// child with it (delegateChild), whatever the parent's own newest proof becomes.
	recordLaunchAuthSession(ctx, a.dbPool, sessionID, proof.SessionID)
	if proof.TokenID != "" {
		if err := db.SetSessionTokenID(ctx, a.dbPool, sessionID, proof.TokenID); err != nil {
			log.Printf("cluster child %s: SetSessionTokenID: %v", sessionID, err)
		}
	}
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
		return apiErrorCause(clusterStartFailureStatus(err), startFailureCause(err), "%s", err.Error())
	}
	clusterJobCreated(ctx, a.hub, a.dbPool, sessionID, a.startWatchEvery())
	return nil
}
