package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

const clusterModelFreshTTL = 15 * time.Minute
const clusterModelCacheLimit = 256

type clusterModelCacheKey struct{ accountID, engine, image string }
type clusterModelCacheEntry struct {
	list               models.List
	refreshing, loaded bool
	retryAt            time.Time
}
type clusterModelCache struct {
	mu      sync.Mutex
	entries map[clusterModelCacheKey]*clusterModelCacheEntry
	// Test seams exercise blocked I/O without a cluster or database.
	load  func(context.Context, string, string, string) (models.List, bool, error)
	store func(context.Context, string, string, string, models.List) error
	probe func(context.Context, string, string, coreProof) (models.List, error)
	now   func() time.Time
}

// WarmClusterModelCache restores persisted catalogs without fetching credentials
// or waiting for a cluster pod. Startup never needs a person's live proof.
func (a *API) WarmClusterModelCache(parent context.Context) {
	// Operator readiness can be warmed without impersonating a person.
	// Personal catalogs still require the caller's own current proof.
	if a.hub != nil && a.hub.JobManager() != nil {
		a.clusterReadinessSnapshot(parent, runnerPrincipal{Kind: runnerKeyPrincipalKind}, false)
	}
	if a.dbPool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	rows, err := db.ListClusterModelCatalogs(ctx, a.dbPool, clusterModelCacheLimit)
	if err != nil {
		log.Print("cluster model cache: could not restore stored catalogs")
		return
	}
	c := &a.clusterModels
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[clusterModelCacheKey]*clusterModelCacheEntry)
	}
	for _, row := range rows {
		if len(c.entries) >= clusterModelCacheLimit {
			break
		}
		key := clusterModelCacheKey{row.AccountID, row.Engine, row.Image}
		if _, ok := c.entries[key]; ok {
			continue
		}
		list, ok := decodeStoredClusterModelList(row.Engine, row.Payload, row.FetchedAt)
		if !ok {
			continue
		}
		c.entries[key] = &clusterModelCacheEntry{list: list, loaded: true}
	}
}

// clusterModelSnapshot never performs credential or Kubernetes I/O on the
// request path. Even the first persistent-cache lookup happens in background.
func (a *API) clusterModelSnapshot(ctx context.Context, engine, accountID string, proof coreProof, force bool) models.List {
	return a.clusterModelSnapshotFor(ctx, engine, accountID, proof, force, false)
}

func (a *API) operatorClusterModelSnapshot(ctx context.Context, engine string, force bool) models.List {
	return a.clusterModelSnapshotFor(ctx, engine, "", coreProof{}, force, true)
}

func (a *API) clusterModelSnapshotFor(ctx context.Context, engine, accountID string, proof coreProof, force, operator bool) models.List {
	engine = models.NormalizeEngine(engine)
	empty := models.List{Models: []models.Model{}, Source: models.SourceNone}
	if !operator && (!proof.valid() || accountID == "") {
		empty.Error = errNoLivenessProof.Error()
		return empty
	}
	jm := a.hub.JobManager()
	if jm == nil {
		empty.Error = "Cluster runtime is unavailable"
		return empty
	}
	key := clusterModelCacheKey{accountID, engine, jm.catalogImageKey(engine)}
	c := &a.clusterModels
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[clusterModelCacheKey]*clusterModelCacheEntry)
	}
	entry := c.entries[key]
	if entry == nil {
		if len(c.entries) >= clusterModelCacheLimit {
			for k, e := range c.entries {
				if !e.refreshing {
					delete(c.entries, k)
					break
				}
			}
		}
		if len(c.entries) >= clusterModelCacheLimit {
			c.mu.Unlock()
			empty.Error = "The model catalog is busy. Try again shortly"
			return empty
		}
		entry = &clusterModelCacheEntry{list: empty}
		c.entries[key] = entry
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	stale := entry.list.FetchedAt == nil || !now.Before(entry.list.FetchedAt.Add(clusterModelFreshTTL))
	start := !entry.refreshing && (force || (stale && !now.Before(entry.retryAt)))
	if start {
		entry.refreshing = true
	}
	list := cloneClusterModelList(entry.list)
	list.Refreshing = entry.refreshing
	c.mu.Unlock()
	if start {
		go a.refreshClusterModelCatalog(context.WithoutCancel(ctx), jm, key, entry, proof, force)
	}
	return list
}

func (a *API) refreshClusterModelCatalog(parent context.Context, jm *JobManager, key clusterModelCacheKey, entry *clusterModelCacheEntry, proof coreProof, force bool) {
	ctx, cancel := context.WithTimeout(parent, 80*time.Second)
	defer cancel()
	c := &a.clusterModels
	c.mu.Lock()
	loaded := entry.loaded
	entry.loaded = true
	c.mu.Unlock()
	if !loaded {
		stored, found, err := a.loadClusterModelCatalog(ctx, key)
		if err == nil && found {
			c.mu.Lock()
			entry.list = cloneClusterModelList(stored)
			now := time.Now()
			if c.now != nil {
				now = c.now()
			}
			fresh := stored.FetchedAt != nil && now.Before(stored.FetchedAt.Add(clusterModelFreshTTL))
			if fresh && !force {
				entry.refreshing = false
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}
	var list models.List
	var err error
	if c.probe != nil {
		list, err = c.probe(ctx, key.engine, key.accountID, proof)
	} else {
		provider, supported := clusterModelProviderFor(key.engine)
		if supported {
			if key.accountID == "" {
				if provider.probeOperator == nil {
					err = errors.New("operator catalog unavailable")
				} else {
					list, err = provider.probeOperator(ctx, jm, force)
				}
			} else {
				list, err = provider.probe(ctx, jm, key.accountID, proof, force)
			}
		} else {
			err = errors.New("engine has no cluster model catalog")
		}
	}
	if err == nil {
		list.Models = models.SanitizeReported(key.engine, list.Models)
		if len(list.Models) == 0 {
			err = errors.New("engine returned an empty model catalog")
		}
	}
	if err == nil {
		list.Source = models.SourceLive
		list.Refreshing = false
		list.Error = ""
		list.DaemonID = ""
		if list.FetchedAt == nil {
			now := time.Now().UTC()
			list.FetchedAt = &now
		}
	}
	c.mu.Lock()
	entry.refreshing = false
	if err != nil {
		entry.list.Error = clusterModelErrorMessage(key.engine, err)
		now := time.Now()
		if c.now != nil {
			now = c.now()
		}
		entry.retryAt = now.Add(clusterModelFreshTTL)
		c.mu.Unlock()
		return
	}
	entry.list = cloneClusterModelList(list)
	entry.retryAt = time.Time{}
	c.mu.Unlock()
	// Publish usable models before touching storage; a slow database must never
	// hide a successful native catalog behind the refresh indicator.
	if storeErr := a.storeClusterModelCatalog(ctx, key, list); storeErr != nil {
		log.Print("cluster model cache: could not persist refreshed catalog")
	}
}

func decodeStoredClusterModelList(engine string, raw json.RawMessage, fetchedAt time.Time) (models.List, bool) {
	var list models.List
	if json.Unmarshal(raw, &list) != nil {
		return models.List{}, false
	}
	list.Models = models.SanitizeReported(engine, list.Models)
	if len(list.Models) == 0 {
		return models.List{}, false
	}
	list.Source = models.SourceLive
	list.FetchedAt = &fetchedAt
	list.Refreshing = false
	list.Error = ""
	list.DaemonID = ""
	return list, true
}
func (a *API) loadClusterModelCatalog(ctx context.Context, key clusterModelCacheKey) (models.List, bool, error) {
	if a.clusterModels.load != nil {
		return a.clusterModels.load(ctx, key.accountID, key.engine, key.image)
	}
	if a.dbPool == nil || key.accountID == "" {
		return models.List{}, false, nil
	}
	raw, at, found, err := db.LoadClusterModelCatalog(ctx, a.dbPool, key.accountID, key.engine, key.image)
	if err != nil || !found {
		return models.List{}, false, err
	}
	list, valid := decodeStoredClusterModelList(key.engine, raw, at)
	return list, valid, nil
}
func (a *API) storeClusterModelCatalog(ctx context.Context, key clusterModelCacheKey, list models.List) error {
	if a.clusterModels.store != nil {
		return a.clusterModels.store(ctx, key.accountID, key.engine, key.image, list)
	}
	if a.dbPool == nil || key.accountID == "" {
		return nil
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return db.StoreClusterModelCatalog(ctx, a.dbPool, key.accountID, key.engine, key.image, raw, *list.FetchedAt)
}
