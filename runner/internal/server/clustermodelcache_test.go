package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

func cacheTestAPI() *API {
	h := NewHub()
	h.SetJobManager(&JobManager{Image: "fixture-image"})
	return &API{hub: h}
}
func cacheFixture(at time.Time, id string) models.List {
	return models.List{Models: []models.Model{{ID: id, Name: id, Efforts: []string{"low", "max"}, DefaultEffort: "low"}}, Source: models.SourceLive, FetchedAt: &at}
}
func awaitCatalog(t *testing.T, a *API, account string) models.List {
	t.Helper()
	end := time.Now().Add(15 * time.Second)
	for time.Now().Before(end) {
		list := a.clusterModelSnapshot(context.Background(), "codex", account, coreProof{SessionID: "fixture-proof"}, false)
		if !list.Refreshing {
			return list
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background catalog did not finish")
	return models.List{}
}

func TestClusterModelSnapshotReturnsImmediatelyAndCoalescesBackgroundWork(t *testing.T) {
	a := cacheTestAPI()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	a.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		calls.Add(1)
		close(started)
		<-release
		return cacheFixture(time.Now(), "fixture-live"), nil
	}
	result := make(chan models.List, 1)
	go func() {
		result <- a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	}()
	select {
	case l := <-result:
		if !l.Refreshing || len(l.Models) != 0 {
			t.Fatalf("wrong initial snapshot: %+v", l)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("request blocked on probe")
	}
	<-started
	for i := 0; i < 5; i++ {
		l := a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, true)
		if !l.Refreshing {
			t.Fatal("lost refresh state")
		}
	}
	close(release)
	l := awaitCatalog(t, a, "account")
	if calls.Load() != 1 || len(l.Models) != 1 || l.Models[0].ID != "fixture-live" {
		t.Fatalf("background refresh: calls=%d list=%+v", calls.Load(), l)
	}
}

func TestClusterModelSnapshotServesStaleOnErrorAndRetriesOnlyWhenAsked(t *testing.T) {
	a := cacheTestAPI()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	a.clusterModels.entries = map[clusterModelCacheKey]*clusterModelCacheEntry{{"account", "codex", "fixture-image"}: {list: cacheFixture(time.Now().Add(-time.Hour), "fixture-stale"), loaded: true}}
	a.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
			return models.List{}, errors.New("fixture-private-error")
		}
		return cacheFixture(time.Now(), "fixture-recovered"), nil
	}
	l := a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	if !l.Refreshing || l.Models[0].ID != "fixture-stale" {
		t.Fatal("stale catalog unavailable during refresh")
	}
	<-started
	close(release)
	l = awaitCatalog(t, a, "account")
	if l.Error != ErrClusterCodexCatalog.Error() || l.Models[0].ID != "fixture-stale" {
		t.Fatalf("failure discarded catalog or exposed error: %+v", l)
	}
	for i := 0; i < 5; i++ {
		l = a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
		if l.Refreshing {
			t.Fatal("normal poll restarted settled failure")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failed refresh repeatedly restarted")
	}
	l = a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, true)
	if !l.Refreshing {
		t.Fatal("explicit retry did not start")
	}
	l = awaitCatalog(t, a, "account")
	if l.Error != "" || l.Models[0].ID != "fixture-recovered" || calls.Load() != 2 {
		t.Fatalf("retry failed: %+v", l)
	}
}

func TestClusterModelSnapshotRestoresPersistedCatalogWithoutProbe(t *testing.T) {
	var mu sync.Mutex
	var stored models.List
	var writes int
	storeDone := make(chan struct{})
	a := cacheTestAPI()
	a.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		return cacheFixture(time.Now(), "fixture-persisted"), nil
	}
	a.clusterModels.store = func(_ context.Context, account, engine, image string, list models.List) error {
		mu.Lock()
		defer mu.Unlock()
		stored = cloneClusterModelList(list)
		writes++
		close(storeDone)
		if account != "account" || image != "fixture-image" {
			t.Error("wrong persistence key")
		}
		return nil
	}
	a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	awaitCatalog(t, a, "account")
	<-storeDone
	b := cacheTestAPI()
	b.clusterModels.load = func(context.Context, string, string, string) (models.List, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return cloneClusterModelList(stored), true, nil
	}
	b.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		t.Error("fresh persisted catalog reprobed")
		return models.List{}, errors.New("unexpected")
	}
	b.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "new-proof"}, false)
	l := awaitCatalog(t, b, "account")
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || l.Models[0].ID != "fixture-persisted" {
		t.Fatal("persistent cache was not restored")
	}
}

func TestClusterModelSnapshotAccountAndImageIsolation(t *testing.T) {
	a := cacheTestAPI()
	a.clusterModels.entries = map[clusterModelCacheKey]*clusterModelCacheEntry{
		{"one", "codex", "fixture-image"}: {list: cacheFixture(time.Now(), "fixture-one"), loaded: true},
		{"two", "codex", "fixture-image"}: {list: cacheFixture(time.Now(), "fixture-two"), loaded: true},
		{"one", "codex", "other-image"}:   {list: cacheFixture(time.Now(), "fixture-other-image"), loaded: true},
	}
	for account, want := range map[string]string{"one": "fixture-one", "two": "fixture-two"} {
		l := a.clusterModelSnapshot(context.Background(), "codex", account, coreProof{SessionID: "fixture-proof"}, false)
		if l.Models[0].ID != want {
			t.Fatal("catalog crossed accounts")
		}
	}
	a.hub.SetJobManager(&JobManager{Image: "other-image"})
	l := a.clusterModelSnapshot(context.Background(), "codex", "one", coreProof{SessionID: "fixture-proof"}, false)
	if l.Models[0].ID != "fixture-other-image" {
		t.Fatal("catalog crossed images")
	}
	l.Models[0].Efforts[0] = "mutated"
	again := a.clusterModelSnapshot(context.Background(), "codex", "one", coreProof{SessionID: "fixture-proof"}, false)
	if again.Models[0].Efforts[0] == "mutated" {
		t.Fatal("caller mutated cached state")
	}
}

func TestClusterModelSnapshotBackgroundSurvivesRequestCancellation(t *testing.T) {
	a := cacheTestAPI()
	started := make(chan struct{})
	release := make(chan struct{})
	a.clusterModels.probe = func(ctx context.Context, _, _ string, _ coreProof) (models.List, error) {
		close(started)
		<-release
		if ctx.Err() != nil {
			t.Error("request cancellation canceled background refresh")
		}
		return cacheFixture(time.Now(), "fixture-live"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.clusterModelSnapshot(ctx, "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	<-started
	cancel()
	close(release)
	awaitCatalog(t, a, "account")
}

func TestClusterModelSnapshotPublishesBeforePersistenceCompletes(t *testing.T) {
	a := cacheTestAPI()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	a.clusterModels.probe = func(context.Context, string, string, coreProof) (models.List, error) {
		return cacheFixture(time.Now(), "fixture-ready"), nil
	}
	a.clusterModels.store = func(context.Context, string, string, string, models.List) error {
		close(started)
		<-release
		close(done)
		return nil
	}
	a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	<-started
	l := a.clusterModelSnapshot(context.Background(), "codex", "account", coreProof{SessionID: "fixture-proof"}, false)
	if l.Refreshing || len(l.Models) != 1 || l.Models[0].ID != "fixture-ready" {
		t.Fatal("database persistence hid ready catalog")
	}
	close(release)
	<-done
}

func TestClusterModelSnapshotScopesEngineAndUsesItsRules(t *testing.T) {
	a := cacheTestAPI()
	var calls atomic.Int32
	a.clusterModels.probe = func(_ context.Context, engine, account string, _ coreProof) (models.List, error) {
		calls.Add(1)
		now := time.Now()
		return models.List{Models: []models.Model{{ID: engine + "-fixture", Name: engine, Efforts: []string{"ultra"}, DefaultEffort: "ultra"}}, Source: models.SourceLive, FetchedAt: &now}, nil
	}
	proof := coreProof{SessionID: "fixture-proof"}
	for _, engine := range []string{"codex", "openclaw"} {
		a.clusterModelSnapshot(context.Background(), engine, "same-account", proof, false)
	}
	read := func(engine string) models.List {
		t.Helper()
		end := time.Now().Add(15 * time.Second)
		for time.Now().Before(end) {
			l := a.clusterModelSnapshot(context.Background(), engine, "same-account", proof, false)
			if !l.Refreshing {
				return l
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("catalog did not refresh")
		return models.List{}
	}
	codex, other := read("codex"), read("openclaw")
	if calls.Load() != 2 || len(codex.Models) != 1 || len(other.Models) != 1 || codex.Models[0].ID != "codex-fixture" || other.Models[0].ID != "openclaw-fixture" {
		t.Fatalf("catalogs crossed engine boundary: calls=%d codex=%+v other=%+v", calls.Load(), codex, other)
	}
	if len(codex.Models[0].Efforts) != 1 || codex.Models[0].DefaultEffort != "ultra" || len(other.Models[0].Efforts) != 0 || other.Models[0].DefaultEffort != "" {
		t.Fatal("catalogs did not apply their engine’s rules")
	}
}

func TestClusterModelProvidersHaveRegisteredRules(t *testing.T) {
	for engine, p := range clusterModelProviders {
		if !models.HasEngineRules(engine) || p.probe == nil || p.safeError == nil {
			t.Fatalf("incomplete cluster catalog provider %q", engine)
		}
	}
}
