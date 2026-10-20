package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const nativeFixtureAccount = "00000000-0000-4000-8000-0000000000a1"

func nativeFixture(t *testing.T) (*reconcileFixture, SessionJobSpec) {
	t.Helper()
	f := newReconcileFixture(t, "starting")
	ctx := context.Background()
	if err := db.SetSessionSpawningAccount(ctx, f.pool, f.sessionID, nativeFixtureAccount); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionEngine(ctx, f.pool, f.sessionID, "codex"); err != nil {
		t.Fatal(err)
	}
	return f, SessionJobSpec{SessionID: f.sessionID, SpawningAccountID: nativeFixtureAccount, Engine: "codex"}
}

func TestNativeHistoryClaimBindingRejectsMissingReplacedAndForeignVolumes(t *testing.T) {
	f, spec := nativeFixture(t)
	var claim map[string]any
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			creates++
			if err := json.NewDecoder(r.Body).Decode(&claim); err != nil {
				t.Error(err)
			}
			meta := claim["metadata"].(map[string]any)
			meta["uid"] = "fixture-native-uid"
			if _, ok := meta["ownerReferences"]; ok {
				t.Error("history must not be owned by a disposable job")
			}
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/persistentvolumeclaims"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case claim == nil:
			w.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(w).Encode(claim)
		}
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool}
	got, err := j.prepareNativeHistory(context.Background(), spec)
	if err != nil || got == nil || creates != 1 {
		t.Fatalf("fresh binding: %+v %v creates=%d", got, err, creates)
	}
	spec.Resume = true
	if _, err = j.prepareNativeHistory(context.Background(), spec); err != nil {
		t.Fatalf("exact binding refused: %v", err)
	}
	claim["metadata"].(map[string]any)["uid"] = "empty-replacement-uid"
	if _, err = j.prepareNativeHistory(context.Background(), spec); err == nil {
		t.Fatal("replacement PVC accepted as exact history")
	}
	claim = nil
	if _, err = j.prepareNativeHistory(context.Background(), spec); err == nil || creates != 1 {
		t.Fatal("missing expected history was recreated")
	}
}

func TestNativeHistoryReplacementWaitsForJobAndAllPods(t *testing.T) {
	const id = "00000000-0000-4000-8000-0000000000b1"
	var deleted atomic.Bool
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pods") {
			if polls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"terminating-fixture"}}]}`))
			} else {
				_, _ = w.Write([]byte(`{"items":[]}`))
			}
			return
		}
		if r.Method == http.MethodDelete {
			var body struct {
				PropagationPolicy string `json:"propagationPolicy"`
				Preconditions     struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.PropagationPolicy != "Foreground" || body.Preconditions.UID != "old-job-uid" {
				t.Error("deletion lacks immutable UID/foreground fence")
			}
			deleted.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if deleted.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": "old-job-uid", "labels": map[string]string{"app": "blerg-runner-agent", "session": id}}})
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client()}
	if err := j.fenceNativeHistoryWriter(context.Background(), SessionJobSpec{SessionID: id, Resume: true}); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() || polls.Load() < 2 {
		t.Fatal("new writer admitted before terminating pods disappeared")
	}
}

func TestNativeHistoryReplacementRefusesForeignJobAndPersistentPods(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "persistent-pod", true: "foreign-job"}[foreign], func(t *testing.T) {
			deletes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes++
					t.Error("unexpected destructive request")
				}
				if strings.Contains(r.URL.Path, "/pods") {
					_, _ = w.Write([]byte(`{"items":[{}]}`))
					return
				}
				if foreign {
					_, _ = w.Write([]byte(`{"metadata":{"uid":"foreign","labels":{"session":"other","app":"blerg-runner-agent"}}}`))
				} else {
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := j.fenceNativeHistoryWriter(ctx, SessionJobSpec{SessionID: "fixture-session"}); err == nil || deletes != 0 {
				t.Fatal("unsafe writer fence accepted")
			}
		})
	}
}

func TestNativeHistoryPodMountsOnlyConversationSubpaths(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		t.Run(engine, func(t *testing.T) {
			pod := map[string]any{"containers": []map[string]any{{"image": "fixture-image"}}, "automountServiceAccountToken": false}
			spec := SessionJobSpec{Engine: engine, nativeHistory: &db.SessionNativeHistory{ClaimName: "fixture-claim"}}
			if err := nativeHistoryPod(spec, pod); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(pod)
			for _, forbidden := range []string{"auth.json", "config.toml", "credentials", "plugins", "ownerReferences"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("nonhistory state mounted: %s", forbidden)
				}
			}
			mounts := pod["containers"].([]map[string]any)[0]["volumeMounts"].([]map[string]any)
			for _, m := range mounts {
				if m["subPath"] == nil {
					t.Fatal("a whole volume is mounted")
				}
				// The engine home itself is pod-ephemeral (engine-home), never the history volume.
				if m["name"] == "native-history" && (m["mountPath"] == "/workspace/.codex" || m["mountPath"] == "/workspace/.claude") {
					t.Fatal("native auth/config parent directory persisted")
				}
			}
			if pod["automountServiceAccountToken"] != false {
				t.Fatal("worker acquired Kubernetes credentials")
			}
		})
	}
}

func TestNativeHistoryDisconnectedSessionDoesNotExpire(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	if err := db.BindSessionNativeHistory(ctx, f.pool, db.SessionNativeHistory{SessionID: spec.SessionID, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(spec.SessionID), ClaimUID: "fixture-retained-uid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status='disconnected',started_at=now()-interval '40 days',status_changed_at=now()-interval '30 days' WHERE id=$1`, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(ctx)
	status, _ := f.status(t)
	if status != "disconnected" {
		t.Fatalf("retained conversation expired after pause: %s", status)
	}
}

func TestNativeHistoryRequiresLiveProofBeforeAnyVolumeAccess(t *testing.T) {
	f, spec := nativeFixture(t)
	spec.AuthSessionID = "fixture-login"
	calls := 0
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(500) }))
	defer kube.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer core.Close()
	j := &JobManager{BaseURL: kube.URL, Namespace: "fixture", Client: kube.Client(), historyPool: f.pool, NativeHistory: NativeHistoryConfig{Enabled: true}, CoreURL: core.URL, CoreInternalKey: "fixture-component-key"}
	if err := j.withNativeHistory(spec); err == nil || calls != 0 {
		t.Fatal("revoked principal accessed retained history")
	}
}

func TestNativeHistoryCapacityRejectsIncompleteOrFullInventory(t *testing.T) {
	for _, body := range []string{`{}`, `{"items":[],"metadata":{"continue":"more"}}`, `{"items":[{},{}]}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("limit") != "2" {
					t.Error("unbounded inventory")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), NativeHistory: NativeHistoryConfig{MaxClaims: 2}}
			if err := j.checkNativeHistoryCapacity(context.Background()); err == nil {
				t.Fatal("unsafe storage admission")
			}
		})
	}
}

func TestNativeHistoryCleanupDeletesOnlyOwnedUIDAfterRetention(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	h := db.SessionNativeHistory{SessionID: spec.SessionID, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(spec.SessionID), ClaimUID: "fixture-cleanup-uid"}
	if err := db.BindSessionNativeHistory(ctx, f.pool, h); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=now()-interval '30 days' WHERE id=$1`, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	deleted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(404)
			return
		}
		if strings.Contains(r.URL.Path, "/pods") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		if r.Method == http.MethodDelete {
			var body struct {
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Preconditions.UID != h.ClaimUID {
				t.Error("cleanup deleted without immutable UID")
			}
			deleted++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": h.ClaimName, "uid": h.ClaimUID, "labels": historyLabels(spec)}})
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool, NativeHistory: NativeHistoryConfig{Enabled: true}}
	if err := j.reclaimNativeHistory(ctx); err != nil || deleted != 1 {
		t.Fatalf("cleanup: %v deletes=%d", err, deleted)
	}
	if err := j.reclaimNativeHistory(ctx); err != nil || deleted != 1 {
		t.Fatalf("cleanup repeated: %v deletes=%d", err, deleted)
	}
	expected, err := db.GetSessionNativeHistory(ctx, f.pool, spec.SessionID)
	if err != nil || expected == nil {
		t.Fatal("lost expected-history tombstone")
	}
}

func TestNativeHistoryFreshRetryDoesNotDeleteExistingWriter(t *testing.T) {
	const id = "00000000-0000-4000-8000-0000000000b2"
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(202)
			return
		}
		if strings.Contains(r.URL.Path, "/pods") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		if deleted {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": "existing-job", "labels": map[string]string{"app": "blerg-runner-agent", "session": id}}})
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client()}
	if err := j.fenceNativeHistoryWriter(context.Background(), SessionJobSpec{SessionID: id}); err == nil || deleted {
		t.Fatal("fresh duplicate spawn replaced an existing writer")
	}
}

func TestNativeHistoryClaimCapacityIsSerializedAcrossSessions(t *testing.T) {
	f, first := nativeFixture(t)
	second := first
	second.SessionID = newUUID()
	ctx := context.Background()
	if err := db.InsertSession(ctx, f.pool, second.SessionID, "00000000-0000-4000-8000-0000000000da", "starting", "/workspace/fixture", "fixture", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, f.pool, second.SessionID, second.SpawningAccountID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionPosture(ctx, f.pool, second.SessionID, "cluster", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionEngine(ctx, f.pool, second.SessionID, "codex"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	claims := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			var claim map[string]any
			if json.NewDecoder(r.Body).Decode(&claim) != nil {
				w.WriteHeader(400)
				return
			}
			meta := claim["metadata"].(map[string]any)
			name := meta["name"].(string)
			meta["uid"] = "uid-" + name
			claims[name] = claim
			w.WriteHeader(201)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/persistentvolumeclaims") {
			list := []any{}
			for _, claim := range claims {
				list = append(list, claim)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": list})
			return
		}
		if claim := claims[path.Base(r.URL.Path)]; claim != nil {
			_ = json.NewEncoder(w).Encode(claim)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool, NativeHistory: NativeHistoryConfig{MaxClaims: 1}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, spec := range []SessionJobSpec{first, second} {
		go func(s SessionJobSpec) { <-start; _, err := j.prepareNativeHistory(ctx, s); results <- err }(spec)
	}
	close(start)
	accepted := 0
	for range 2 {
		if err := <-results; err == nil {
			accepted++
		}
	}
	mu.Lock()
	count := len(claims)
	mu.Unlock()
	if accepted != 1 || count != 1 {
		t.Fatalf("concurrent starts exceeded retained-claim capacity: accepted=%d claims=%d", accepted, count)
	}
}

func TestNativeHistoryUUIDAliasesCannotBypassExpectedBinding(t *testing.T) {
	f, spec := nativeFixture(t)
	canonical := spec.SessionID[:35] + "a"
	if _, err := f.pool.Exec(context.Background(), `UPDATE sessions SET id=$2 WHERE id=$1`, spec.SessionID, canonical); err != nil {
		t.Fatal(err)
	}
	spec.SessionID = canonical
	h := db.SessionNativeHistory{SessionID: canonical, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(canonical), ClaimUID: "fixture-alias-claim"}
	if err := db.BindSessionNativeHistory(context.Background(), f.pool, h); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool}
	// A static operator resume has no account proof. Alternate UUID spellings
	// must not skip the binding lookup and fall through to legacy job creation.
	spec.SpawningAccountID = ""
	spec.Resume = true
	for _, enabled := range []bool{false, true} {
		j.NativeHistory.Enabled = enabled
		for _, alias := range []string{strings.ToUpper(canonical), strings.ReplaceAll(canonical, "-", ""), "{" + canonical + "}"} {
			spec.SessionID = alias
			if err := j.withNativeHistory(spec); err == nil {
				t.Error("UUID alias accepted without owner proof")
			}
		}
	}
	if calls != 0 {
		t.Fatalf("alternate UUID spellings bypassed expected-history gate: %d Kubernetes calls", calls)
	}
}

// One claim that can never be reclaimed (here: its UID changed) must not stop
// cleanup of the others behind it.
func TestNativeHistoryCleanupSkipsAFailingClaim(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	bad := "00000000-0000-4000-8000-0000000000bd"
	if _, err := f.pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,engine,runtime,spawning_account_id) SELECT $2,daemon_id,status,project_path,repo,engine,runtime,spawning_account_id FROM sessions WHERE id=$1`, spec.SessionID, bad); err != nil {
		t.Fatal(err)
	}
	good := db.SessionNativeHistory{SessionID: spec.SessionID, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(spec.SessionID), ClaimUID: "fixture-good-uid"}
	stale := db.SessionNativeHistory{SessionID: bad, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(bad), ClaimUID: "fixture-stale-uid"}
	for _, h := range []db.SessionNativeHistory{good, stale} {
		if err := db.BindSessionNativeHistory(ctx, f.pool, h); err != nil {
			t.Fatal(err)
		}
	}
	// The failing claim is the oldest, so it is listed first.
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=now()-interval '30 days' WHERE id=$1`, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=now()-interval '40 days' WHERE id=$1`, bad); err != nil {
		t.Fatal(err)
	}
	deleted := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(404)
			return
		}
		if strings.Contains(r.URL.Path, "/pods") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if r.Method == http.MethodDelete {
			deleted[name] = true
			w.WriteHeader(http.StatusAccepted)
			return
		}
		h, s := good, spec
		if name == stale.ClaimName {
			h, s = stale, SessionJobSpec{SessionID: bad, SpawningAccountID: spec.SpawningAccountID, Engine: "codex"}
			h.ClaimUID = "fixture-replaced-uid"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": h.ClaimName, "uid": h.ClaimUID, "labels": historyLabels(s)}})
	}))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool, NativeHistory: NativeHistoryConfig{Enabled: true}}
	if err := j.reclaimNativeHistory(ctx); err == nil {
		t.Fatal("a failed claim was not reported")
	}
	if !deleted[good.ClaimName] || deleted[stale.ClaimName] {
		t.Fatalf("cleanup stopped at the failing claim or deleted it: %v", deleted)
	}
}
