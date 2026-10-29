package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Exercise the real Secret allocation boundary, including the helper's actual
// ten-second deadline rather than cancellation before any POST is submitted.
func TestLaunchAmbiguousSecretCleanup(t *testing.T) {
	for _, mode := range []string{"deadline", "canceled", "unavailable", "foreign", "replaced", "delete_refused", "late_commit", "accepted_delete", "deleting", "absent"} {
		t.Run(mode, func(t *testing.T) {
			f, spec := nativeFixture(t)
			spec.AuthSessionID = "fixture-login"
			plan := clusterPlanFixture()
			plan.SessionID, plan.Manifest.OwnerID = spec.SessionID, spec.SpawningAccountID
			if err := db.InsertSessionWorkspace(t.Context(), f.pool, spec.SessionID, plan.Manifest); err != nil {
				t.Fatal(err)
			}
			generation, err := db.BeginWorkspaceRuntime(t.Context(), f.pool, spec.SessionID, plan.Manifest.LaunchGeneration, 0)
			if err != nil {
				t.Fatal(err)
			}
			plan.Runtime.Generation = generation
			spec.workspacePlan, spec.workspaceClaim = &plan, "fixture-workspace"
			j := newTestJobManager(t, f.fake)
			j.historyPool = f.pool
			f.fake.setJobMissing(j.jobName(spec.SessionID))
			var mu sync.Mutex
			var object map[string]any
			var creates, deletes, reads int
			unavailable := mode == "unavailable"
			refuseDelete := mode == "delete_refused"
			lateVisible := false
			fallback := f.fake.handler()
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/secrets/"+sessionSecretName(spec.SessionID)) || (r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/secrets")) {
					mu.Lock()
					defer mu.Unlock()
					switch r.Method {
					case http.MethodPost:
						creates++
						if json.NewDecoder(r.Body).Decode(&object) != nil {
							t.Error("invalid Secret")
						}
						object["metadata"].(map[string]any)["uid"] = "owned-secret-uid"
						w.WriteHeader(http.StatusCreated)
					case http.MethodGet:
						reads++
						if unavailable {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if object == nil || mode == "absent" || (mode == "late_commit" && !lateVisible) {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						raw, _ := json.Marshal(object)
						var found map[string]any
						_ = json.Unmarshal(raw, &found)
						meta := found["metadata"].(map[string]any)
						if mode == "foreign" {
							meta["annotations"].(map[string]any)[launchCreateAnnotation] = "foreign-attempt"
						}
						if mode == "replaced" && reads >= 2 {
							meta["uid"] = "replacement-uid"
						}
						_ = json.NewEncoder(w).Encode(found)
					case http.MethodDelete:
						var options struct {
							Preconditions struct {
								UID string `json:"uid"`
							} `json:"preconditions"`
						}
						if json.NewDecoder(r.Body).Decode(&options) != nil || options.Preconditions.UID != "owned-secret-uid" {
							t.Error("cleanup lacks exact owned UID precondition")
						}
						if refuseDelete {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						deletes++
						if mode == "deleting" {
							object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-25T00:00:00Z"
							w.WriteHeader(http.StatusOK)
							return
						}
						object = nil
						if mode == "accepted_delete" {
							w.WriteHeader(http.StatusAccepted)
							return
						}
						w.WriteHeader(http.StatusOK)
					default:
						t.Error("unexpected Secret mutation")
					}
					return
				}
				fallback(w, r)
			}))
			defer kube.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := kube.Client()
			base := client.Transport
			client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/secrets/") && ctx.Err() != nil {
					deadline, ok := r.Context().Deadline()
					if !ok || r.Context().Err() != nil || time.Until(deadline) > 10*time.Second {
						t.Error("recovery did not use an independent bounded context")
					}
				}
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/secrets") {
					return base.RoundTrip(r)
				}
				resp, err := base.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				_ = resp.Body.Close()
				if mode == "deadline" {
					<-r.Context().Done()
				} else {
					cancel()
				}
				return nil, r.Context().Err()
			})
			j.BaseURL, j.Client = kube.URL, client

			if err := j.createSessionJobContext(ctx, spec); err == nil {
				t.Fatal("ambiguous expired launch succeeded")
			}
			mu.Lock()
			gotDeletes, gotCreates := deletes, creates
			mu.Unlock()
			uncertain := mode == "unavailable" || mode == "foreign" || mode == "replaced" || mode == "delete_refused" || mode == "late_commit" || mode == "accepted_delete" || mode == "deleting" || mode == "absent"
			if !uncertain && gotDeletes != 1 {
				t.Fatalf("Secret leaked after ambiguous create: creates=%d deletes=%d", gotCreates, gotDeletes)
			}
			if uncertain && gotDeletes != 0 && mode != "accepted_delete" && mode != "deleting" {
				t.Fatal("uncertain Secret was deleted")
			}
			if gotCreates != 1 || len(f.fake.created) != 0 {
				t.Fatal("duplicated launch allocation", gotCreates, len(f.fake.created))
			}

			generation, _, err = db.GetWorkspaceRuntime(t.Context(), f.pool, spec.SessionID)
			if err != nil || generation != 1 {
				t.Fatal("cleanup advanced the runtime generation", generation, err)
			}
			var capabilities int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM board_tokens WHERE session_id=$1`, spec.SessionID).Scan(&capabilities); err != nil || capabilities != 2 {
				t.Fatal("launch allocated extra tokens", capabilities, err)
			}
			var pending int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM launch_secret_cleanup`).Scan(&pending); err != nil {
				t.Fatal("durable cleanup intent unavailable", err)
			}
			if uncertain {
				if pending != 1 {
					t.Fatal("unresolved allocation has no durable cleanup obligation", pending)
				}

				var metadata string
				if err := f.pool.QueryRow(t.Context(), `SELECT metadata::text FROM launch_secret_cleanup`).Scan(&metadata); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"stringData", "BLERG_RUNNER_RUNTIME_TOKEN", "BLERG_RUNNER_SESSION_TOKEN"} {
					if strings.Contains(metadata, key) {
						t.Fatal("cleanup intent stored credential material")
					}
				}
				if mode == "unavailable" {
					// Ordinary session deletion first removes its non-cascading event log.
					if _, err := f.pool.Exec(t.Context(), `DELETE FROM agent_events WHERE session_id=$1`, spec.SessionID); err != nil {
						t.Fatal(err)
					}
					if _, err := f.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, spec.SessionID); err != nil {
						t.Fatal(err)
					}
				}
				mu.Lock()
				unavailable, refuseDelete, lateVisible = false, false, true
				if mode == "deleting" {
					object = nil
				}
				mu.Unlock()
				// Simulate process replacement: the new manager knows only persisted
				// metadata, never the original request or credential values.
				restarted := &JobManager{BaseURL: j.BaseURL, Namespace: j.Namespace, Client: j.Client, historyPool: f.pool}
				if _, err := f.pool.Exec(t.Context(), `UPDATE launch_secret_cleanup SET ready_after=now()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
				f.api.hub.SetJobManager(restarted)
				f.api.reconcileClusterJobsOnce(t.Context())
				mu.Lock()
				gotDeletes = deletes
				mu.Unlock()
				switch mode {
				case "foreign", "replaced":
					if gotDeletes != 0 {
						t.Fatal("reconciler deleted foreign/replaced Secret")
					}
				case "absent":
					if gotDeletes != 0 {
						t.Fatal("absent allocation deleted by name")
					}
				default:
					if gotDeletes != 1 {
						t.Fatal("reconciler abandoned owned Secret", gotDeletes)
					}
				}
				if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM launch_secret_cleanup`).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if mode == "foreign" || mode == "replaced" {
					if pending != 1 {
						t.Fatal("uncertainty discarded")
					}
				} else if pending != 0 {
					t.Fatal("completed retry retained obligation", pending)
				}
			} else if pending != 0 {
				t.Fatal("completed cleanup intent retained", pending)
			}
		})
	}
}

func TestLaunchRollbackDiagnosticsExcludePrivateDetails(t *testing.T) {
	for _, mode := range []string{"delete_body", "delete_transport", "patch_body", "patch_transport", "retry_exhaustion"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeK8s{jobUID: "fixture-job-uid"}
			j := newTestJobManager(t, f)
			spec := SessionJobSpec{SessionID: "private-object-sentinel", NoRepo: true, InitialPrompt: "private-token-sentinel"}
			f.setJobMissing(j.jobName(spec.SessionID))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			base := j.Client.Transport
			j.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") && !strings.HasPrefix(mode, "patch_") {
					if mode == "retry_exhaustion" {
						cancel()
						return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(`{"reason":"Conflict"}`)), Header: http.Header{}}, nil
					}
					return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"reason":"Forbidden"}`)), Header: http.Header{}}, nil
				}
				if (r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/secrets/")) || (r.Method == http.MethodPatch && strings.HasPrefix(mode, "patch_")) {
					if strings.HasSuffix(mode, "transport") {
						return nil, errors.New("https://private-url-sentinel/private-object-sentinel")
					}
					return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"reason":"Forbidden","message":"private-token-sentinel https://private-url-sentinel/private-object-sentinel"}`)), Header: http.Header{}}, nil
				}
				return base.RoundTrip(r)
			})
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			err := j.createSessionJobContext(ctx, spec)
			output := logs.String()
			if err != nil {
				output += err.Error()
				w := httptest.NewRecorder()
				writeError(w, 503, err.Error())
				output += w.Body.String()
			}
			for _, secret := range []string{"private-token-sentinel", "private-url-sentinel", "private-object-sentinel"} {
				if strings.Contains(output, secret) {
					t.Errorf("private detail escaped launch diagnostics: %s", secret)
				}
			}
		})
	}
}

func TestLaunchRollbackStageDiagnosticsExcludePrivateDetails(t *testing.T) {
	f, spec := nativeFixture(t)
	spec.NoRepo, spec.AuthSessionID = true, "fixture-login"
	j := newTestJobManager(t, f.fake)
	j.historyPool = f.pool
	f.fake.setJobMissing(j.jobName(spec.SessionID))
	base := j.Client.Transport
	j.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") || r.Method == http.MethodDelete {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"reason":"Forbidden","message":"private-token-sentinel https://private-url-sentinel/private-object-sentinel"}`)), Header: http.Header{}}, nil
		}
		return base.RoundTrip(r)
	})
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	progress := clusterLaunchProgress{j: j, session: spec.SessionID}
	progress.next(t.Context(), "queued", "Submitting the consumer Job")
	err := j.createSessionJobContext(t.Context(), spec)
	if err == nil {
		t.Fatal("rejected Job succeeded")
	}
	progress.finish(t.Context(), &err)
	stages, readErr := clusterLaunchSnapshot(t.Context(), f.pool, spec.SessionID)
	if readErr != nil || len(stages) != 1 || stages[0].Reason != "Forbidden" || stages[0].Outcome != "failed" {
		t.Fatal("lost safe primary failure stage", stages, readErr)
	}
	raw, _ := json.Marshal(stages)
	w := httptest.NewRecorder()
	writeError(w, 503, err.Error())
	output := logs.String() + string(raw) + w.Body.String()
	for _, sentinel := range []string{"private-token-sentinel", "private-url-sentinel", "private-object-sentinel", sessionSecretName(spec.SessionID), j.jobName(spec.SessionID), j.BaseURL} {
		if strings.Contains(output, sentinel) {
			t.Fatal("private detail escaped rollback diagnostics")
		}
	}
}

// The HTTP owner check is deliberately separate from this manager boundary:
// callers using the manager directly must receive the same private refusal.
func TestOwnerShellNonownerRefusalCodes(t *testing.T) {
	for _, state := range []string{"live", "not_ready", "policy_disabled", "unavailable", "fenced"} {
		t.Run(state, func(t *testing.T) {
			rt := &countedClusterShellRuntime{shellTestRuntime: &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 1)}}
			if state == "not_ready" {
				rt.binding.Live = false
			}
			audit := make(shellAuditReceipt, 16)
			m, err := ownershell.New(shellTestAuthority{}, rt, rt, audit, nil)
			if err != nil {
				t.Fatal(err)
			}
			if state != "unavailable" {
				m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: state != "policy_disabled"})
			}
			if state == "fenced" {
				m.NotifyRuntimePhase(t.Context(), ownershell.SupervisorIdentity(rt.binding, ownershell.SignedGrant{}).Launch, "stopped")
			}
			defer m.Unavailable(context.Background())
			_, err = m.Open(t.Context(), ownershell.Actor{Kind: "human", Owner: "another-owner", Login: "another-login"}, "session")
			if !errors.Is(err, ownershell.ErrDenied) || ownershell.RefusalReason(err) != ownershell.ReasonUnavailable {
				t.Fatal("manager leaked private state", err)
			}
			if (<-audit).Reason != ownershell.ReasonUnavailable || rt.opens.Load() != 0 {
				t.Fatal("private refusal audit or runtime dispatch changed")
			}
		})
	}
}

func TestLaunchSecretCleanupPersistenceRequiredBeforePOST(t *testing.T) {
	f, _ := nativeFixture(t)
	j := newTestJobManager(t, f.fake)
	j.historyPool = f.pool
	if _, err := f.pool.Exec(t.Context(), `DROP TABLE launch_secret_cleanup`); err != nil {
		t.Fatal(err)
	}
	if _, err := j.createSessionSecret(t.Context(), f.sessionID, map[string][]byte{"fixture-key": []byte("fixture-secret")}); err == nil {
		t.Fatal("allocated credentials without durable recovery")
	}
	if len(f.fake.createdSecrets) != 0 {
		t.Fatal("Secret POST preceded durable obligation")
	}
}

func TestLaunchCredentialDiscoveryDiagnosticsPrivate(t *testing.T) {
	j := newTestJobManager(t, &fakeK8s{})
	j.SecretName, j.OAuthSecretName = "private-object-sentinel", "private-object-sentinel-oauth"
	j.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://private-url-sentinel/private-token-sentinel")
	})}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	if engines := j.AvailableEngines(); len(engines) != 0 {
		t.Fatal("unavailable credential discovery advertised an engine")
	}
	for _, sentinel := range []string{"private-object-sentinel", "private-url-sentinel", "private-token-sentinel"} {
		if strings.Contains(logs.String(), sentinel) {
			t.Fatal("credential discovery logged private data")
		}
	}
}
