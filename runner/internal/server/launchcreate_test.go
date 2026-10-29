package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func quotaConflict(w http.ResponseWriter) {
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "reason": "Conflict", "details": map[string]string{"kind": "resourcequotas"}, "message": "Operation cannot be fulfilled on resourcequotas: fixture-private object has been modified"})
}

func TestLaunchCreatesRetryQuotaConflicts(t *testing.T) {
	for _, workspace := range []bool{true, false} {
		t.Run(map[bool]string{true: "workspace", false: "native_history"}[workspace], func(t *testing.T) {
			f, spec := nativeFixture(t)
			p := clusterPlanFixture()
			p.SessionID, p.Manifest.OwnerID = spec.SessionID, spec.SpawningAccountID
			if workspace {
				if err := db.InsertSessionWorkspace(t.Context(), f.pool, spec.SessionID, p.Manifest); err != nil {
					t.Fatal(err)
				}
				spec.Workspace, spec.WorkspaceMembers = &p.Manifest, p.Members
			} else {
				spec.Repo = "org/project"
			}
			spec.AuthSessionID = "fixture-login"
			f.fake.credentialKinds = []string{"github"}
			f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-personal-git")}
			f.fake.pods = map[string]map[string]any{}
			f.fake.jobUID = "fixture-job-uid"
			j := newTestJobManager(t, f.fake)
			j.historyPool = f.pool
			j.WorkspaceStorageClass = "fixture-wffc"
			j.NativeHistory = NativeHistoryConfig{Enabled: !workspace, StorageClass: "fixture-wffc", Size: "1Gi", MaxClaims: 100}
			j.ExcludedNodes = []string{"excluded-node"}
			f.fake.setJobMissing(j.jobName(spec.SessionID))
			var mu sync.Mutex
			var claim map[string]any
			posts := map[string]int{}
			bodies := map[string][]byte{}
			fallback := f.fake.handler()
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(raw))
					mu.Lock()
					posts[r.URL.Path]++
					n := posts[r.URL.Path]
					if n == 1 {
						bodies[r.URL.Path] = raw
					} else if !bytes.Equal(raw, bodies[r.URL.Path]) {
						t.Error("retry changed fenced create payload")
					}
					mu.Unlock()
					if n <= 2 {
						quotaConflict(w)
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/pods") {
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
					return
				}
				if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
					mu.Lock()
					defer mu.Unlock()
					if r.Method == http.MethodPost {
						if json.NewDecoder(r.Body).Decode(&claim) != nil {
							t.Error("invalid claim")
							return
						}
						claim["metadata"].(map[string]any)["uid"] = "fixture-claim-uid"
						claim["status"] = map[string]any{"phase": "Pending", "reason": "WaitForFirstConsumer"}
						w.WriteHeader(http.StatusCreated)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/persistentvolumeclaims") {
						_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
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
			if err := j.CreateSessionJob(spec); err != nil {
				t.Fatal("quota contention refused launch", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != 3 {
				t.Fatalf("did not exercise all launch creates: %v", posts)
			}
			for path, n := range posts {
				if n != 3 {
					t.Errorf("%s: %d attempts, expected 3", path, n)
				}
			}
			if len(f.fake.created) != 1 || len(f.fake.createdSecrets) != 1 {
				t.Fatal("retries created extra Jobs or Secrets")
			}
			if workspace {
				generation, _, err := db.GetWorkspaceRuntime(context.Background(), f.pool, spec.SessionID)
				if err != nil || generation != 1 {
					t.Fatal("retry advanced the fenced generation", generation, err)
				}
			}
		})
	}
}

func TestLaunchCreateConflictDeadlineKeepsStageReason(t *testing.T) {
	f, spec := nativeFixture(t)
	j := newTestJobManager(t, f.fake)
	j.historyPool = f.pool
	for _, stageName := range []string{"storage", "secrets", "queued"} {
		t.Run(stageName, func(t *testing.T) {
			var posts atomic.Int32
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); quotaConflict(w) }))
			defer kube.Close()
			j.BaseURL, j.Client = kube.URL, kube.Client()
			progress := clusterLaunchProgress{j: j, session: spec.SessionID}
			progress.next(t.Context(), stageName, "Creating launch object")
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			body := launchTestObject("Job", j.Namespace)
			_, err := j.createLaunchObject(ctx, "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs", body)
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("persistent Conflict did not reach deadline")
			}
			var failure *jobSubmissionError
			if !errors.As(err, &failure) || failure.reason != "Conflict" || posts.Load() < 1 {
				t.Fatal("lost bounded conflict failure", err, posts.Load())
			}
			progress.finish(ctx, &err)
			stages, err := clusterLaunchSnapshot(t.Context(), f.pool, spec.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			last := stages[len(stages)-1]
			if last.Stage != stageName || last.Reason != "Conflict" || last.Outcome != "failed" || last.FinishedAt.IsZero() {
				t.Fatal("deadline hid Conflict stage", last)
			}
		})
	}
}

func launchTestObject(kind, ns string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "fixture-create", "namespace": ns, "labels": map[string]string{"app": "fixture-launch", "session": "fixture-session"}}, "stringData": map[string]string{"credential": "fixture-private-token"}}
}

type launchCreateTransport func(*http.Request) (*http.Response, error)

func (f launchCreateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLaunchCreateAmbiguousResponseVerifiesOwnershipAndUID(t *testing.T) {
	for _, mode := range []string{"owned", "not_created", "already_exists_owned", "already_exists_foreign", "foreign_marker", "foreign_label", "foreign_owner", "empty_uid", "replaced_uid", "deleted", "foreign_namespace", "read_forbidden"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var object map[string]any
			posts, reads, creates := 0, 0, 0
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodPost:
					posts++
					if object != nil {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]string{"reason": "AlreadyExists"})
						return
					}
					creates++
					if json.NewDecoder(r.Body).Decode(&object) != nil {
						t.Error("invalid create body")
						return
					}
					object["metadata"].(map[string]any)["uid"] = "fixture-created-uid"
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"uid": "fixture-created-uid"}})
				case http.MethodGet:
					reads++
					if strings.HasPrefix(mode, "already_exists_") && reads == 1 {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if mode == "read_forbidden" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if object == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					// Copy so a changed read cannot accidentally mutate our desired fixture.
					raw, _ := json.Marshal(object)
					var found map[string]any
					_ = json.Unmarshal(raw, &found)
					meta := found["metadata"].(map[string]any)
					switch mode {
					case "foreign_marker", "already_exists_foreign":
						meta["annotations"].(map[string]any)[launchCreateAnnotation] = "another-invocation"
					case "foreign_label":
						meta["labels"].(map[string]any)["session"] = "another-session"
					case "foreign_owner":
						meta["ownerReferences"] = []map[string]string{{"uid": "another-owner"}}
					case "empty_uid":
						meta["uid"] = ""
					case "replaced_uid":
						if reads == 2 {
							meta["uid"] = "replacement-uid"
						}
					case "deleted":
						meta["deletionTimestamp"] = "2026-10-25T00:00:00Z"
					case "foreign_namespace":
						meta["namespace"] = "foreign-namespace"
					}
					_ = json.NewEncoder(w).Encode(found)
				default:
					t.Error("recovery modified an existing object")
				}
			}))
			defer kube.Close()
			client := kube.Client()
			base := client.Transport
			var attempts atomic.Int32
			client.Transport = launchCreateTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || attempts.Add(1) != 1 {
					return base.RoundTrip(r)
				}
				if mode != "not_created" {
					resp, err := base.RoundTrip(r)
					if err != nil {
						return nil, err
					}
					_ = resp.Body.Close()
				}
				return nil, errors.New("fixture-private-transport-url")
			})
			j := &JobManager{BaseURL: kube.URL, Client: client, Namespace: "fixture"}
			resp, err := j.createLaunchObject(t.Context(), "/api/v1/namespaces/fixture/secrets", launchTestObject("Secret", "fixture"))
			success := mode == "owned" || mode == "not_created" || mode == "already_exists_owned"
			if success {
				if err != nil || resp.StatusCode != http.StatusCreated {
					t.Fatal("owned create not recovered", err)
				}
				raw, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if strings.Contains(string(raw), "fixture-private-token") {
					t.Fatal("recovery forwarded Secret data")
				}
			} else {
				var failure *jobSubmissionError
				if !errors.As(err, &failure) || (failure.reason != "AlreadyExists" && failure.reason != "Forbidden") || strings.Contains(err.Error(), "fixture-private") {
					t.Fatal("foreign/uncertain object accepted", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantPosts := 1
			if strings.HasPrefix(mode, "already_exists_") {
				wantPosts = 2
			}
			if posts != wantPosts || creates != 1 {
				t.Fatalf("created %d physical objects in %d POSTs", creates, posts)
			}
			if mode == "already_exists_owned" && reads != 3 {
				t.Fatal("did not verify UID after AlreadyExists", reads)
			}
			if mode == "owned" && (reads != 2 || attempts.Load() != 1) {
				t.Fatal("recovery did not pin UID before success", reads, attempts.Load())
			}
			if mode == "not_created" && attempts.Load() != 2 {
				t.Fatal("missing create was not retried")
			}
		})
	}
}

func TestLaunchCreateDoesNotRetryForeignOrUnclassifiedRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason string
		status int
	}{{"AlreadyExists", 409}, {"Forbidden", 403}, {"Invalid", 422}, {"", 409}, {"fixture-private", 409}} {
		t.Run(tc.reason, func(t *testing.T) {
			var posts atomic.Int32
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("read/adopted a foreign initial conflict")
				}
				posts.Add(1)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"reason": tc.reason, "message": "fixture-private"})
			}))
			defer kube.Close()
			j := &JobManager{BaseURL: kube.URL, Client: kube.Client(), Namespace: "fixture"}
			resp, err := j.createLaunchObject(t.Context(), "/api/v1/namespaces/fixture/secrets", launchTestObject("Secret", "fixture"))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status || posts.Load() != 1 {
				t.Fatal("refusal retried or adopted")
			}
		})
	}
}

func TestLaunchCreateBackoffIsBounded(t *testing.T) {
	for _, attempt := range []int{0, 1, 4, 10, 10000} {
		delay := launchCreateBackoff(attempt)
		if delay < 25*time.Millisecond || delay > time.Second {
			t.Fatal("unbounded jitter", attempt, delay)
		}
	}
}

func TestLaunchCreateQuotaConflictsInModelProbe(t *testing.T) {
	f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
	var mu sync.Mutex
	attempts := map[string]int{}
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			attempts[r.URL.Path]++
			n := attempts[r.URL.Path]
			mu.Unlock()
			if n <= 2 {
				quotaConflict(w)
				return
			}
		}
		f.serve(w, r)
	}))
	defer kube.Close()
	j := &JobManager{BaseURL: kube.URL, Client: kube.Client(), Namespace: "fixture", Image: "fixture-agent:version"}
	result, err := j.probeClusterCodexModels(t.Context(), []byte("fixture-private-credential"))
	if err != nil || len(result.Models) != 2 {
		t.Fatal("quota conflict refused model probe", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatal("probe did not exercise both creates")
	}
	for _, n := range attempts {
		if n != 3 {
			t.Fatal("probe did not retry quota conflicts", n)
		}
	}
	if f.jobs != 1 || f.secrets != 1 || f.jobDeletes != 1 || f.secretDeletes != 1 {
		t.Fatal("probe leaked or duplicated resources")
	}
}

func TestLaunchCreateExpiredParentDoesNotSubmit(t *testing.T) {
	var posts atomic.Int32
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(http.StatusCreated) }))
	defer kube.Close()
	j := &JobManager{BaseURL: kube.URL, Client: kube.Client(), Namespace: "fixture"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := j.createLaunchObject(ctx, "/api/v1/namespaces/fixture/secrets", launchTestObject("Secret", "fixture")); err == nil || posts.Load() != 0 {
		t.Fatal("expired parent created an object", err)
	}
}

func TestLaunchCreateSuccessResponseRemainsReadable(t *testing.T) {
	sendBody := make(chan struct{})
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.(http.Flusher).Flush()
		select {
		case <-sendBody:
			_, _ = io.WriteString(w, `{"metadata":{"uid":"fixture-created-uid"}}`)
		case <-r.Context().Done():
		}
	}))
	defer kube.Close()
	client := kube.Client()
	base := client.Transport
	var requestCtx context.Context
	client.Transport = launchCreateTransport(func(r *http.Request) (*http.Response, error) { requestCtx = r.Context(); return base.RoundTrip(r) })
	j := &JobManager{BaseURL: kube.URL, Client: client, Namespace: "fixture"}
	resp, err := j.createLaunchObject(t.Context(), "/api/v1/namespaces/fixture/secrets", launchTestObject("Secret", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	close(sendBody)
	if requestCtx.Err() != nil {
		t.Fatal("create canceled its response before the caller could read it")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(raw), "fixture-created-uid") {
		t.Fatal("created object UID was truncated", err)
	}
	_ = resp.Body.Close()
	if requestCtx.Err() == nil {
		t.Fatal("response close did not release the create deadline")
	}
}
