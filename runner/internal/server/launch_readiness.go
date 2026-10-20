package server

import (
	"context"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
)

type launchReadinessKey struct {
	account, token, session, image string
	personalOnly                   bool
}
type launchReadinessEntry struct {
	engines           []string
	known, refreshing bool
	updated           time.Time
}

// discoverCredentialEngines preserves unknown transport/RBAC state and propagates
// the shared deadline through every Secret lookup. It reads only operator scope.
func (j *JobManager) discoverCredentialEngines(ctx context.Context) ([]string, bool) {
	if j.Client == nil || j.BaseURL == "" {
		return nil, false
	}
	names := []string{j.SecretName}
	if j.OAuthSecretName != "" && j.OAuthSecretName != j.SecretName {
		names = append(names, j.OAuthSecretName)
	}
	keys := map[string]bool{}
	for _, name := range names {
		if name == "" {
			continue
		}
		found, err := j.fetchSecretKeysContext(ctx, name)
		if err != nil {
			return nil, false
		}
		for key, present := range found {
			keys[key] = keys[key] || present
		}
	}
	out := []string{}
	for _, descriptor := range engines.All() {
		if !descriptor.SupportsRuntime(runnerRuntimeCluster) {
			continue
		}
		for _, key := range descriptor.CredentialEnvKeys {
			if keys[key] {
				out = append(out, descriptor.ID)
				break
			}
		}
	}
	return j.withSharedConfigEngines(out), true
}

type launchReadinessCache struct {
	mu      sync.Mutex
	entries map[launchReadinessKey]*launchReadinessEntry
	slots   chan struct{}
}

// clusterReadinessSnapshot never performs remote I/O on the request path.
// Proof identity is part of the cache key: another token cannot inherit a
// successful credential lookup from a revoked or differently scoped proof.
func (a *API) clusterReadinessSnapshot(ctx context.Context, p RunnerPrincipal, force bool, personalOnly ...bool) ([]string, bool, bool) {
	j := a.hub.JobManager()
	if j == nil {
		return nil, true, false
	}
	proof := p.proof()
	key := launchReadinessKey{account: p.spawningAccountID(), token: proof.TokenID, session: proof.SessionID, image: j.Image, personalOnly: len(personalOnly) > 0 && personalOnly[0]}
	cache := &a.launchReady
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[launchReadinessKey]*launchReadinessEntry{}
		cache.slots = make(chan struct{}, 2)
	}
	entry := cache.entries[key]
	if entry == nil {
		if len(cache.entries) >= 256 {
			for k, old := range cache.entries {
				if !old.refreshing {
					delete(cache.entries, k)
					break
				}
			}
			if len(cache.entries) >= 256 {
				cache.mu.Unlock()
				return nil, false, false
			}
		}
		entry = &launchReadinessEntry{}
		cache.entries[key] = entry
	}
	start := !entry.refreshing && (force || time.Since(entry.updated) > 30*time.Second)
	if start {
		entry.refreshing = true
	}
	kinds, known, refreshing := append([]string{}, entry.engines...), entry.known, entry.refreshing
	cache.mu.Unlock()
	if start {
		go func() {
			background, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			select {
			case cache.slots <- struct{}{}:
			case <-background.Done():
				cache.mu.Lock()
				entry.refreshing = false
				entry.updated = time.Now()
				cache.mu.Unlock()
				return
			}
			defer func() { <-cache.slots }()
			var available []string
			verified := true
			if !key.personalOnly {
				available, verified = j.discoverCredentialEngines(background)
			}
			if key.personalOnly && (key.account == "" || a.coreURL == "" || !proof.valid()) {
				verified = false
			}
			if key.account != "" && a.coreURL != "" {
				lookup, cancelLookup := context.WithTimeout(withCoreProof(background, proof), 3*time.Second)
				personal, _, ok := a.listPersonalCredentialKinds(lookup, key.account)
				cancelLookup()
				verified = verified && ok
				if ok {
					available = append(available, personal...)
				}
			}
			cache.mu.Lock()
			entry.known, entry.refreshing, entry.updated = verified, false, time.Now()
			if verified {
				entry.engines = available
			} else {
				entry.engines = nil
			}
			cache.mu.Unlock()
			if verified && p.Kind == runnerKeyPrincipalKind {
				for _, engine := range available {
					if provider, ok := clusterModelProviderOn(engine, j); ok && provider.probeOperator != nil {
						a.operatorClusterModelSnapshot(background, engine, false)
					}
				}
			}
		}()
	}
	return kinds, known, refreshing
}
