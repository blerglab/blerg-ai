package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClusterWorkspaceStorageModes(t *testing.T) {
	for _, tc := range []struct{ name, bounded, fallback, mode string }{
		{"cluster default", "", "", "standard"},
		{"standard class", "", "local-x", "standard"},
		{"bounded class wins", "fixture-bounded", "local-x", "bounded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, spec := nativeFixture(t)
			p := clusterPlanFixture()
			p.SessionID, p.Manifest.OwnerID = spec.SessionID, spec.SpawningAccountID
			if err := db.InsertSessionWorkspace(t.Context(), f.pool, spec.SessionID, p.Manifest); err != nil {
				t.Fatal(err)
			}
			f.fake.credentialKinds = []string{"github"}
			f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-personal-git")}
			f.fake.pods = map[string]map[string]any{}
			j := newTestJobManager(t, f.fake)
			j.historyPool = f.pool
			j.WorkspaceStorageClass = tc.bounded
			j.WorkspaceDefaultClass = tc.fallback
			f.fake.setJobMissing(j.jobName(spec.SessionID))
			var mu sync.Mutex
			var claim map[string]any
			creates := 0
			var mutateMode string
			fallback := f.fake.handler()
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods") {
					mu.Lock()
					mode := mutateMode
					mutateMode = ""
					mu.Unlock()
					if mode != "" {
						if _, err := f.pool.Exec(r.Context(), `UPDATE cluster_workspace_storage SET storage_mode=$2 WHERE session_id=$1`, spec.SessionID, mode); err != nil {
							t.Error(err)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
					return
				}
				if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
					mu.Lock()
					defer mu.Unlock()
					if r.Method == http.MethodPost {
						creates++
						if err := json.NewDecoder(r.Body).Decode(&claim); err != nil {
							t.Error(err)
						}
						claim["metadata"].(map[string]any)["uid"] = "fixture-workspace-uid"
						claim["status"] = map[string]any{"phase": "Pending"}
						w.WriteHeader(http.StatusCreated)
						return
					}
					if claim == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(claim)
					return
				}
				fallback(w, r)
			}))
			defer kube.Close()
			j.BaseURL, j.Client = kube.URL, kube.Client()
			spec.AuthSessionID, spec.Repo, spec.GitURL = "fixture-login", "", ""
			spec.Workspace, spec.WorkspaceMembers = &p.Manifest, p.Members
			if err := j.CreateSessionJob(spec); err != nil {
				t.Fatal(err)
			}
			wantClass := tc.bounded
			if wantClass == "" {
				wantClass = tc.fallback
			}
			mu.Lock()
			actualClass, present := claim["spec"].(map[string]any)["storageClassName"]
			mu.Unlock()
			if wantClass == "" {
				if present {
					t.Fatal("storageClassName must be absent with no resolved class", actualClass)
				}
			} else if !present || actualClass != wantClass {
				t.Fatalf("class=%v want %s", actualClass, wantClass)
			}
			assertMode := func() {
				t.Helper()
				var storedMode string
				if err := f.pool.QueryRow(t.Context(), `SELECT storage_mode FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&storedMode); err != nil || storedMode != tc.mode {
					t.Fatalf("stored mode=%q: %v", storedMode, err)
				}
				f.fake.mu.Lock()
				secret := f.fake.createdSecrets[len(f.fake.createdSecrets)-1]
				f.fake.mu.Unlock()
				raw := secret["stringData"].(map[string]any)[clusterworkspace.Env].(string)
				var plan map[string]any
				if err := json.Unmarshal([]byte(raw), &plan); err != nil {
					t.Fatal(err)
				}
				flag, _ := plan["unbounded_storage"].(bool)
				if flag != (tc.mode == "standard") {
					t.Fatalf("plan unbounded_storage=%v for %s", plan["unbounded_storage"], tc.mode)
				}
				if tc.mode == "bounded" {
					if _, exists := plan["unbounded_storage"]; exists {
						t.Fatal("bounded flag must be omitted")
					}
				}
			}
			assertMode()
			identities := []clusterworkspace.MemberIdentity{}
			for i, m := range p.Members {
				identities = append(identities, clusterworkspace.MemberIdentity{ID: m.ID, Directory: protocol.WorkspaceRoot{Device: 1, Inode: uint64(i + 1)}, GitDirectory: protocol.WorkspaceRoot{Device: 1, Inode: uint64(i + 10)}, Objects: protocol.WorkspaceRoot{Device: 1, Inode: uint64(i + 20)}})
			}
			raw, _ := json.Marshal(identities)
			if _, err := f.pool.Exec(t.Context(), `UPDATE cluster_workspace_storage SET member_identities=$2 WHERE session_id=$1`, spec.SessionID, raw); err != nil {
				t.Fatal(err)
			}
			// Both configuration transitions preserve the stored mode and claim.
			if tc.mode == "bounded" {
				j.WorkspaceStorageClass = ""
			} else {
				j.WorkspaceStorageClass = "new-bounded-class"
			}
			j.WorkspaceDefaultClass = "changed-default"
			spec.Resume, spec.WorkspaceMembers = true, nil
			if err := j.CreateSessionJob(spec); err != nil {
				t.Fatalf("resume after setting changed: %v", err)
			}
			assertMode()
			mu.Lock()
			count := creates
			afterClass, afterPresent := claim["spec"].(map[string]any)["storageClassName"]
			mu.Unlock()
			if count != 1 || afterClass != actualClass || afterPresent != present {
				t.Fatal("resume replaced or reclassified retained claim")
			}
			// Change a valid mode after the first read but before the runtime fence.
			other := "bounded"
			if tc.mode == "bounded" {
				other = "standard"
			}
			mu.Lock()
			mutateMode = other
			mu.Unlock()
			if err := j.CreateSessionJob(spec); !errors.Is(err, errClusterWorkspace) {
				t.Fatalf("mode changed under fence: %v", err)
			}
			// Simulate a corrupt/future mode that bypassed the database constraint.
			if _, err := f.pool.Exec(t.Context(), `ALTER TABLE cluster_workspace_storage DROP CONSTRAINT cluster_workspace_storage_storage_mode_check`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE cluster_workspace_storage SET storage_mode='unknown' WHERE session_id=$1`, spec.SessionID); err != nil {
				t.Fatal(err)
			}
			if err := j.CreateSessionJob(spec); !errors.Is(err, errClusterWorkspace) {
				t.Fatalf("unknown mode accepted: %v", err)
			}
			f.fake.mu.Lock()
			jobs := len(f.fake.created)
			f.fake.mu.Unlock()
			if jobs != 2 {
				t.Fatalf("refused modes submitted jobs: %d", jobs)
			}
		})
	}
}
