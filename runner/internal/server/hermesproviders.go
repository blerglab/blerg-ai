package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// The cluster's shared Hermes provider configuration: one Hermes config.yaml
// fragment (providers, custom_providers, model defaults) the operator sets at
// install time (install/k8s, HERMES_PROVIDERS_FILE → Secret
// blerg-hermes-providers → this server's BLERG_RUNNER_HERMES_PROVIDERS). It is
// delivered to every Hermes session pod through the per-session Secret and
// merged there into ~/.hermes/config.yaml (internal/runner/hermesconfig.go).
// The server also reads it to offer the providers' models and to say whether
// Hermes on the cluster runs on local hardware. It is treated as a secret: a
// fragment may carry a provider key, so no value from it is ever logged,
// returned or written into a Job spec.

// hermesProvidersEnvVar names the fragment both on this server and in the pod
// (daemon.HermesProvidersEnvVar). A BLERG_RUNNER_ name, so callers' ExtraEnv
// can never supply it.
const hermesProvidersEnvVar = "BLERG_RUNNER_HERMES_PROVIDERS"

// hermesProvidersMaxBytes bounds the fragment. It travels as one environment
// entry (to this server, and to every Hermes pod), and Linux refuses to start
// a process with any single entry over 128 KiB; the pod and deploy.sh enforce
// the same 64 KiB.
const hermesProvidersMaxBytes = 64 << 10

// ErrClusterHermesCatalog is the only text a failed shared-provider catalog
// shows a browser.
var ErrClusterHermesCatalog = errors.New("could not load the cluster's shared Hermes models. Check that the model servers are reachable from the runner")

// HermesSharedConfig is the shared fragment plus the two functions that read
// it the way the desktop daemon reads ~/.hermes/config.yaml. cmd/server wires
// the daemon's own implementations, so routing IDs, display names and the
// local rule match the desktop exactly; this package cannot import the daemon.
type HermesSharedConfig struct {
	Config string
	Probe  func(context.Context, []byte) ([]models.Model, error)
	Local  func([]byte) bool

	// operatorEnv caches whether the operator Secret carries a Hermes .env
	// (which could add provider keys), for HermesSharedLocal. Guarded by the
	// JobManager's engineCacheMu, like the other Secret-sweep caches.
	operatorEnvAt time.Time
	operatorEnv   bool
	// personal caches personalHermesCredential's answer per owner and proof (same lock).
	personal map[string]hermesPersonalEntry
}

type hermesPersonalEntry struct {
	has, ok bool
	until   time.Time
}

// hermesPersonalTTL is how long an owner's credential-kind answer is reused; a failed lookup is
// retried sooner. hermesPersonalCacheMax bounds the map.
const (
	hermesPersonalTTL      = 30 * time.Second
	hermesPersonalFailTTL  = 5 * time.Second
	hermesPersonalCacheMax = 256
)

// hermesSharedFromEnv reads the fragment from the server's environment. Blank
// means not configured; an oversized fragment is ignored, with a log line that
// names only the problem.
func hermesSharedFromEnv() HermesSharedConfig {
	raw := os.Getenv(hermesProvidersEnvVar)
	if strings.TrimSpace(raw) == "" {
		return HermesSharedConfig{}
	}
	if len(raw) > hermesProvidersMaxBytes {
		log.Printf("cluster: %s is larger than 64 KiB; ignoring the shared Hermes provider configuration (Hermes on the cluster falls back to personal .env files)", hermesProvidersEnvVar)
		return HermesSharedConfig{}
	}
	return HermesSharedConfig{Config: raw}
}

// hermesSharedConfigured reports whether a shared fragment is set.
func (j *JobManager) hermesSharedConfigured() bool {
	return j != nil && j.Hermes.Config != ""
}

// sharedConfigEngines are the engines the shared configuration alone makes
// usable on the cluster: Hermes against keyless (local) model servers needs no
// .env, so the fragment counts like a credential for availability.
func (j *JobManager) sharedConfigEngines() []string {
	if !j.hermesSharedConfigured() {
		return nil
	}
	if d, ok := engines.Lookup("hermes"); !ok || !d.SupportsRuntime(runnerRuntimeCluster) {
		return nil
	}
	return []string{"hermes"}
}

// withSharedConfigEngines adds sharedConfigEngines to found, once each.
func (j *JobManager) withSharedConfigEngines(found []string) []string {
	for _, id := range j.sharedConfigEngines() {
		if !slices.Contains(found, id) {
			found = append(found, id)
		}
	}
	return found
}

// addHermesSharedSessionEnv puts the fragment into a Hermes session's
// per-session Secret data and returns the Job env entry that references it.
// Nothing for any other engine, when no fragment is configured, or for a
// session that must never run on operator-supplied material (a cron's): the
// fragment may carry an operator provider key.
func (j *JobManager) addHermesSharedSessionEnv(spec SessionJobSpec, engine string, sessionData map[string][]byte) (map[string]any, error) {
	if _, dup := spec.ExtraEnv[hermesProvidersEnvVar]; dup {
		return nil, errors.New("env key " + hermesProvidersEnvVar + " is reserved")
	}
	if engine != "hermes" || !j.hermesSharedConfigured() || spec.NoOperatorFallback {
		return nil, nil
	}
	sessionData[hermesProvidersEnvVar] = []byte(j.Hermes.Config)
	return map[string]any{
		"name": hermesProvidersEnvVar,
		"valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": hermesProvidersEnvVar, "optional": false},
		},
	}, nil
}

// hermesSharedModels probes the shared providers' model servers from this
// server, with the desktop's probe. The list is the same for every account.
func (j *JobManager) hermesSharedModels(ctx context.Context) (models.List, error) {
	if !j.hermesSharedConfigured() || j.Hermes.Probe == nil {
		return models.List{}, ErrClusterHermesCatalog
	}
	list, err := j.Hermes.Probe(ctx, []byte(j.Hermes.Config))
	if err != nil || len(list) == 0 {
		// The probe's error may name an endpoint; only the fixed text leaves.
		return models.List{}, ErrClusterHermesCatalog
	}
	now := time.Now().UTC()
	return models.List{Models: list, Source: models.SourceLive, FetchedAt: &now}, nil
}

// catalogImageKey is the image part of a cluster catalog's cache key. For
// Hermes it also names the shared fragment (by digest, never by content), so a
// changed configuration is never answered from the old one's stored list.
func (j *JobManager) catalogImageKey(engine string) string {
	if models.NormalizeEngine(engine) != "hermes" || !j.hermesSharedConfigured() {
		return j.Image
	}
	sum := sha256.Sum256([]byte(j.Hermes.Config))
	return j.Image + "#hermes-providers-" + hex.EncodeToString(sum[:8])
}

// HermesSharedLocal applies the desktop's local rule to Hermes on the cluster
// before any pod exists, for what every account shares: the fragment routes
// only to loopback or private-network endpoints, none of its providers carries
// a key, the operator Secret holds no Hermes .env that could add one, and no
// uploaded config bundle carries a ~/.hermes/config.yaml that pods would merge
// the fragment onto. A person's own saved Hermes credential is the caller's to
// check (clusterBudgetFor); a running Hermes pod reports its exact answer.
// Fails closed.
func (j *JobManager) HermesSharedLocal(ctx context.Context) bool {
	if !j.hermesSharedConfigured() || j.Hermes.Local == nil || !j.Hermes.Local([]byte(j.Hermes.Config)) {
		return false
	}
	if bundled, ok := agentBundleHasHermesConfig(); bundled || !ok {
		return false
	}
	h := &j.Hermes
	j.engineCacheMu.Lock()
	fresh := !h.operatorEnvAt.IsZero() && time.Since(h.operatorEnvAt) < clusterSecretsCacheTTL
	hasEnv := h.operatorEnv
	j.engineCacheMu.Unlock()
	if !fresh {
		names := []string{j.SecretName}
		if j.OAuthSecretName != "" && j.OAuthSecretName != j.SecretName {
			names = append(names, j.OAuthSecretName)
		}
		hasEnv = false
		for _, name := range names {
			keys, err := j.fetchSecretKeysContext(ctx, name)
			if err != nil {
				return false // cannot tell: not local
			}
			hasEnv = hasEnv || keys["HERMES_ENV_CONTENTS"]
		}
		j.engineCacheMu.Lock()
		h.operatorEnvAt, h.operatorEnv = time.Now(), hasEnv
		j.engineCacheMu.Unlock()
	}
	return !hasEnv
}

// clusterLocalEngines is the cluster's local-hardware rule for one owner's sessions: the engines a
// child of parent, started with p's proof, runs on local hardware. It feeds the cluster budget and
// local_only rule matching alike (budgetForCluster, childHost.localEngine). Only Hermes can be:
//
//   - the shared rule (HermesSharedLocal) must hold and the owner must have no saved Hermes
//     credential, which every Hermes pod of theirs gets as ~/.hermes/.env and which may hold a
//     provider key (the desktop rule's "not local"). It is asked of core with p's proof (the
//     parent's recorded launch proof for the agent's own calls); a lookup that cannot answer is
//     "not local";
//   - when the parent itself is a connected Hermes pod, its own report can only take "local"
//     away, never grant it: its files are the agent's to edit, and a child gets the owner's saved
//     .env, not the parent's copy.
func (a *API) clusterLocalEngines(ctx context.Context, parent *db.SessionRow, p runnerPrincipal) []string {
	jm := a.hub.JobManager()
	if jm == nil || parent == nil {
		return nil
	}
	personal := func(ctx context.Context) (bool, bool) {
		return jm.personalHermesCredential(ctx, p.spawningAccountID(), p.proof())
	}
	if hermesLocalOnCluster(ctx, jm, a.hub.FindDaemonForSession(parent.ID), models.NormalizeEngine(derefOrEmpty(parent.Engine)), personal) {
		return []string{"hermes"}
	}
	return nil
}

// hermesLocalOnCluster is clusterLocalEngines' rule for Hermes. pod is the parent's own pod
// connection (nil while away) and podEngine its engine; personal reports whether the owner saved a
// Hermes credential (has) and whether that could be told at all (ok). nil personal: not local.
func hermesLocalOnCluster(ctx context.Context, j *JobManager, pod *DaemonConn, podEngine string, personal func(context.Context) (has, ok bool)) bool {
	if pod != nil && podEngine == "hermes" && slices.Contains(pod.AvailableEngines(), "hermes") && !pod.LocalEngine("hermes") {
		return false
	}
	if personal == nil || !j.HermesSharedLocal(ctx) {
		return false
	}
	has, ok := personal(ctx)
	return ok && !has
}

// personalHermesCredential asks core whether account saved a Hermes credential, with proof. With no
// personal credentials wired in none can reach a pod: (false, true). No account or proof, or a core
// that cannot answer: (false, false).
// The answer is cached for hermesPersonalTTL per account and proof.
func (j *JobManager) personalHermesCredential(ctx context.Context, account string, proof coreProof) (has, ok bool) {
	if !j.personalCredentialsWired() {
		return false, true
	}
	key := account + "\x00" + proof.TokenID + "\x00" + proof.SessionID
	j.engineCacheMu.Lock()
	if e, hit := j.Hermes.personal[key]; hit && time.Now().Before(e.until) {
		j.engineCacheMu.Unlock()
		return e.has, e.ok
	}
	j.engineCacheMu.Unlock()
	kinds, err := listCorePersonalKinds(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, account, proof)
	entry := hermesPersonalEntry{has: slices.Contains(kinds, "hermes"), ok: err == nil, until: time.Now().Add(hermesPersonalTTL)}
	if err != nil {
		entry = hermesPersonalEntry{until: time.Now().Add(hermesPersonalFailTTL)}
	}
	j.engineCacheMu.Lock()
	if j.Hermes.personal == nil || len(j.Hermes.personal) >= hermesPersonalCacheMax {
		j.Hermes.personal = map[string]hermesPersonalEntry{}
	}
	j.Hermes.personal[key] = entry
	j.engineCacheMu.Unlock()
	return entry.has, entry.ok
}

// expireHermesPersonalCache drops every cached owner answer (tests, and a changed configuration).
func (j *JobManager) expireHermesPersonalCache() {
	j.engineCacheMu.Lock()
	j.Hermes.personal = nil
	j.engineCacheMu.Unlock()
}

// agentBundleHasHermesConfig reports whether the stored agent-config bundle
// (agentconfig.go) carries .hermes/config.yaml. ok is false when the bundle
// exists but cannot be read. No bundle: (false, true).
func agentBundleHasHermesConfig() (has, ok bool) {
	f, err := os.Open(agentConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return false, false
	}
	// The bundle is replaced by rename on every upload: its path, size and mtime name a version,
	// so a scan is reused until it changes.
	stamp := bundleStamp{path: agentConfigPath(), size: st.Size(), mod: st.ModTime()}
	bundleScan.mu.Lock()
	if bundleScan.stamp == stamp {
		has, ok = bundleScan.has, bundleScan.ok
		bundleScan.mu.Unlock()
		return has, ok
	}
	bundleScan.mu.Unlock()
	has, ok = scanBundleForHermesConfig(f)
	bundleScan.mu.Lock()
	bundleScan.stamp, bundleScan.has, bundleScan.ok = stamp, has, ok
	bundleScan.mu.Unlock()
	return has, ok
}

type bundleStamp struct {
	path string
	size int64
	mod  time.Time
}

var bundleScan struct {
	mu      sync.Mutex
	stamp   bundleStamp
	has, ok bool
}

func scanBundleForHermesConfig(f *os.File) (has, ok bool) {
	gz, err := gzip.NewReader(io.LimitReader(f, maxConfigBundle+1))
	if err != nil {
		return false, false
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, true
		}
		if err != nil {
			return false, false
		}
		if path.Clean(strings.TrimPrefix(hdr.Name, "./")) == ".hermes/config.yaml" {
			return true, true
		}
	}
}
