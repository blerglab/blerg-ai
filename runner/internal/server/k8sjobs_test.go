package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// fakeK8s records job API calls and lets tests control the active-job count
// and the contents of any Secret objects fetchSecretKeys asks for (keyed by
// Secret name, values already base64-encoded as a real k8s API response
// would have them).
type fakeK8s struct {
	mu             sync.Mutex
	created        []map[string]any
	deleted        []string
	createdSecrets []map[string]any
	deletedSecrets []string
	// patchedSecrets records merge patches applied to Secrets, keyed by
	// Secret name — how CreateSessionJob attaches the per-session Secret's
	// ownerReferences to the created Job.
	patchedSecrets map[string]map[string]any
	// jobUID is the metadata.uid the fake reports for every created Job.
	jobUID string
	active int
	// failNext makes the next Job-create POST fail with 500; jobCreateBody is
	// the body it answers with — a real k8s rejection echoes the manifest it
	// refused, which is what must never reach the caller.
	failNext      bool
	jobCreateBody string
	secrets       map[string]map[string]string // secret name -> base64-encoded data

	// credentialFetchResponses stands in for Task 15's internal
	// credentials-fetch endpoint on blerg-core: "<accountID>:<engine>" ->
	// plaintext credential bytes. A missing key means "404 not found" —
	// the normal case for an account with no personal credential.
	credentialFetchResponses map[string][]byte
	// credentialFetchCalls counts every request the credential handler
	// received, regardless of outcome — lets a test assert the endpoint was
	// actually hit (e.g. on a resumed session) without depending on the
	// side effect of a Secret being created.
	credentialFetchCalls int
	// credentialFetchTokenIDs records the token_id each credential request
	// carried ("" when it carried none), so a test can assert the session's
	// own agent token — not just its account — reached core.
	credentialFetchTokenIDs []string
	// credentialFetchSessionIDs records the session_id of each credential request (empty for an agent-token one).
	credentialFetchSessionIDs []string
	// pluginLists stands in for core's POST /internal/plugins/list: accountID ->
	// the plugin list; a missing account answers 404. pluginStatus, when set,
	// answers every plugin request with that status instead. pluginRequests
	// records each request body received.
	pluginLists    map[string][]pluginspec.Entry
	pluginStatus   int
	pluginRequests []internalPluginsRequest
	// conflictJob, when set, makes the next Job-create POST return 409 as if
	// a live Job with the same name already existed (the "double spawn"
	// branch of CreateSessionJob's 409 handling — jobFinished's GET for that
	// job name reports succeeded=0/failed=0 by default, i.e. "still live").
	conflictJob bool
	// secretCreateStatus/secretCreateBody, when set, make every Secret-create
	// POST fail with that status and body — how a real k8s API rejects an
	// invalid Secret, echoing request detail back in the message.
	secretCreateStatus int
	secretCreateBody   string
	// jobStatuses/missingJobs let a test say what the cluster reports for one
	// named Job: a status object (succeeded/failed/active/conditions) or a 404.
	// Unlisted names keep the default "still live" answer.
	jobStatuses map[string]map[string]any
	missingJobs map[string]bool
	// pods is what the pod list answers, keyed by session id (one pod per
	// session). nil answers 403, like a cluster without the pods RBAC rule.
	pods map[string]map[string]any
}

// setPod makes the fake report pod (a Pod object) for sessionID.
func (f *fakeK8s) setPod(sessionID string, pod map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pods == nil {
		f.pods = map[string]map[string]any{}
	}
	f.pods[sessionID] = pod
}

// setJobStatus makes the fake report this status for the named Job.
func (f *fakeK8s) setJobStatus(name string, status map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobStatuses == nil {
		f.jobStatuses = map[string]map[string]any{}
	}
	f.jobStatuses[name] = status
}

// setJobMissing makes the fake 404 the named Job, as k8s does once a Job has
// been garbage-collected or never existed.
func (f *fakeK8s) setJobMissing(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missingJobs == nil {
		f.missingJobs = map[string]bool{}
	}
	f.missingJobs[name] = true
}

const testInternalKey = "test-internal-key"

func (f *fakeK8s) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pods"):
			// The start watcher's pod list (startstages.go). A fake that was
			// never given pods answers 403, as a cluster without the pods
			// RBAC rule does, and the watcher then says nothing.
			if f.pods == nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			sel := r.URL.Query().Get("labelSelector")
			var items []map[string]any
			for sid, p := range f.pods {
				if strings.Contains(sel, "session="+sid) {
					items = append(items, p)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/secrets/"):
			parts := strings.Split(r.URL.Path, "/")
			name := parts[len(parts)-1]
			data, ok := f.secrets[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/secrets"):
			if f.secretCreateStatus != 0 {
				w.WriteHeader(f.secretCreateStatus)
				_, _ = w.Write([]byte(f.secretCreateBody))
				return
			}
			body, _ := io.ReadAll(r.Body)
			var secret map[string]any
			_ = json.Unmarshal(body, &secret)
			f.createdSecrets = append(f.createdSecrets, secret)
			w.WriteHeader(201)
		case r.Method == "PATCH" && strings.Contains(r.URL.Path, "/secrets/"):
			body, _ := io.ReadAll(r.Body)
			var patch map[string]any
			_ = json.Unmarshal(body, &patch)
			parts := strings.Split(r.URL.Path, "/")
			if f.patchedSecrets == nil {
				f.patchedSecrets = map[string]map[string]any{}
			}
			f.patchedSecrets[parts[len(parts)-1]] = patch
			w.WriteHeader(200)
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/secrets/"):
			parts := strings.Split(r.URL.Path, "/")
			f.deletedSecrets = append(f.deletedSecrets, parts[len(parts)-1])
			w.WriteHeader(200)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/jobs") && r.URL.RawQuery != "":
			// List with labelSelector — ActiveSessionJobs's cap check.
			items := make([]map[string]any, 0, f.active)
			for i := 0; i < f.active; i++ {
				items = append(items, map[string]any{"status": map[string]any{"active": 1}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/jobs"):
			// A specific Job by name — jobFinished's lookup after a 409, and
			// the cluster reconciler's poll. Unless a test said otherwise the
			// answer is "still live" (succeeded=0, failed=0).
			parts := strings.Split(r.URL.Path, "/")
			name := parts[len(parts)-1]
			if f.missingJobs[name] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			status, ok := f.jobStatuses[name]
			if !ok {
				status = map[string]any{"succeeded": 0, "failed": 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
		case r.Method == "POST":
			if f.failNext {
				f.failNext = false
				w.WriteHeader(500)
				_, _ = w.Write([]byte(f.jobCreateBody))
				return
			}
			if f.conflictJob {
				f.conflictJob = false
				w.WriteHeader(http.StatusConflict)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var job map[string]any
			_ = json.Unmarshal(body, &job)
			f.created = append(f.created, job)
			uid := f.jobUID
			if uid == "" {
				uid = "job-uid-1"
			}
			w.WriteHeader(201)
			// A real k8s API returns the created object, including the uid
			// CreateSessionJob needs to build the Secret's ownerReferences.
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": uid}})
		case r.Method == "DELETE":
			parts := strings.Split(r.URL.Path, "/")
			f.deleted = append(f.deleted, parts[len(parts)-1])
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}
}

// credentialHandler stands in for blerg-core's POST /internal/credentials/fetch
// (Task 15): requires the shared internal-key header and looks up
// "<accountID>:<engine>" in credentialFetchResponses, 404ing when absent.
func (f *fakeK8s) credentialHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/plugins/list" {
			f.pluginHandler(w, r)
			return
		}
		f.mu.Lock()
		f.credentialFetchCalls++
		f.mu.Unlock()
		if r.Header.Get("X-Internal-Key") != testInternalKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			AccountID string `json:"account_id"`
			Engine    string `json:"engine"`
			TokenID   string `json:"token_id"`
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Core's rule: exactly one named live proof, else 400.
		if (body.TokenID != "") == (body.SessionID != "") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.credentialFetchTokenIDs = append(f.credentialFetchTokenIDs, body.TokenID)
		f.credentialFetchSessionIDs = append(f.credentialFetchSessionIDs, body.SessionID)
		plaintext, ok := f.credentialFetchResponses[body.AccountID+":"+body.Engine]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"plaintext_base64": base64.StdEncoding.EncodeToString(plaintext),
		})
	}
}

func newTestJobManager(t *testing.T, f *fakeK8s) *JobManager {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	credSrv := httptest.NewServer(f.credentialHandler())
	t.Cleanup(credSrv.Close)
	return &JobManager{
		BaseURL: srv.URL, Token: "tok", Namespace: "blerg-runner-sessions",
		Image: "blerg-runner-devcontainer:test", Client: srv.Client(),
		ServerWSURL: "ws://server/ws/daemon", ServerHTTPURL: "http://server",
		SecretName: "blerg-runner-agent", GitURLBase: "https://github.com/org",
		MaxSessions: 2, PodTTLSeconds: 3600,
		CPURequest: "500m", MemRequest: "1Gi", CPULimit: "1", MemLimit: "4Gi",
		TerminationGraceSeconds: 120, TTLSecondsAfterFinished: 3600,
		CoreURL: credSrv.URL, CoreInternalKey: testInternalKey, CredentialClient: credSrv.Client(),
	}
}

func TestCreateSessionJobManifest(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "abc-123", Repo: "proj", Title: "T", Model: "claude-sonnet-5",
		InitialPrompt: "build it", Resume: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created = %d", len(f.created))
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{
		`"name":"blerg-runner-agent-abc-123"`,
		`"BLERG_RUNNER_SESSION_ID","value":"abc-123"`,
		`"BLERG_RUNNER_GIT_URL","value":"https://github.com/org/proj.git"`,
		`"restartPolicy":"Never"`,
		`"terminationGracePeriodSeconds":120`,
		`"activeDeadlineSeconds":3600`,
		`"secretKeyRef"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %s", want)
		}
	}
	if strings.Contains(body, "BLERG_RUNNER_RESUME") {
		t.Error("fresh spawn must not set BLERG_RUNNER_RESUME")
	}
}

func TestCreateSessionJobEngine(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.OAuthSecretName = "blerg-runner-oauth"
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s", Repo: "r", Engine: "codex"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{
		`"BLERG_RUNNER_ENGINE","value":"codex"`,
		`"name":"CODEX_AUTH_JSON"`,
		`"key":"CODEX_AUTH_JSON","name":"blerg-runner-oauth"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %s\n%s", want, body)
		}
	}
}

func TestCreateSessionJobHermesEngine(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.OAuthSecretName = "blerg-runner-oauth"
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s", Repo: "r", Engine: "hermes"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{
		`"BLERG_RUNNER_ENGINE","value":"hermes"`,
		`"name":"HERMES_ENV_CONTENTS"`,
		`"key":"HERMES_ENV_CONTENTS","name":"blerg-runner-oauth"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %s\n%s", want, body)
		}
	}
}

func TestCreateSessionJobResumeFlag(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s", Repo: "r", Resume: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"BLERG_RUNNER_RESUME","value":"1"`) {
		t.Error("resume flag missing")
	}
}

func TestCreateSessionJobCap(t *testing.T) {
	f := &fakeK8s{active: 2}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s", Repo: "r"})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want cap error, got %v", err)
	}
	if len(f.created) != 0 {
		t.Fatal("job created despite cap")
	}
}

func TestDeleteSessionJob(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.DeleteSessionJob("abc-123"); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "blerg-runner-agent-abc-123" {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestCreateSessionJobUsesPersonalCredentialWhenAvailable(t *testing.T) {
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-oat01-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s1", Repo: "r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected 1 per-session secret created, got %d", len(f.createdSecrets))
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), `"name":"blerg-runner-session-s1"`) {
		t.Errorf("per-session secret must be named blerg-runner-session-s1, got %s", secretRaw)
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"key":"CLAUDE_CODE_OAUTH_TOKEN","name":"blerg-runner-session-s1"`) {
		t.Errorf("Job must reference the per-session secret name for CLAUDE_CODE_OAUTH_TOKEN, got %s", raw)
	}
}

// TestCreateSessionJobPersonalGitHubToken: a launching account's personal
// GitHub credential (credential kind "github") must reach the pod as
// BLERG_RUNNER_GIT_TOKEN from the per-session Secret, never from the shared
// operator Secret — that token is what runner.go injects into the clone URL,
// so cloning happens as the human who launched the session.
func TestCreateSessionJobPersonalGitHubToken(t *testing.T) {
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:github": []byte("ghp_personal"),
		},
	}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-gh", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected 1 per-session secret, got %d", len(f.createdSecrets))
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), `"BLERG_RUNNER_GIT_TOKEN":"ghp_personal"`) {
		t.Errorf("per-session secret must carry the personal git token, got %s", secretRaw)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if !strings.Contains(body, `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-session-s-gh","optional":false`) {
		t.Errorf("BLERG_RUNNER_GIT_TOKEN must come from the per-session secret, got %s", body)
	}
	if strings.Contains(body, `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
		t.Errorf("BLERG_RUNNER_GIT_TOKEN must not also be sourced from the operator secret: %s", body)
	}
	if strings.Contains(body, "ghp_personal") {
		t.Errorf("the git token must never appear as a literal value in the Job spec: %s", body)
	}
}

// TestCreateSessionJobPersonalClaudeAPIKey: a personal claude credential that
// is an API key (anything not prefixed sk-ant-oat) becomes ANTHROPIC_API_KEY,
// and CLAUDE_CODE_OAUTH_TOKEN must not be sourced at all — otherwise an
// operator OAuth token would shadow the user's own API-key billing.
func TestCreateSessionJobPersonalClaudeAPIKey(t *testing.T) {
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-api03-personal"),
		},
	}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-api", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), `"ANTHROPIC_API_KEY":"sk-ant-api03-personal"`) {
		t.Errorf("personal API key must land under ANTHROPIC_API_KEY, got %s", secretRaw)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if !strings.Contains(body, `"key":"ANTHROPIC_API_KEY","name":"blerg-runner-session-s-api"`) {
		t.Errorf("ANTHROPIC_API_KEY must come from the per-session secret, got %s", body)
	}
	if strings.Contains(body, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN must be absent entirely with a personal API key: %s", body)
	}
}

// TestCreateSessionJobPersonalClaudeOAuth: the sk-ant-oat prefix means a
// subscription OAuth token, which runner.go turns into subscription mode; the
// operator's ANTHROPIC_API_KEY must not be sourced alongside it.
func TestCreateSessionJobPersonalClaudeOAuth(t *testing.T) {
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-oat01-personal"),
		},
	}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-oat", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), `"CLAUDE_CODE_OAUTH_TOKEN":"sk-ant-oat01-personal"`) {
		t.Errorf("personal OAuth token must land under CLAUDE_CODE_OAUTH_TOKEN, got %s", secretRaw)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if !strings.Contains(body, `"key":"CLAUDE_CODE_OAUTH_TOKEN","name":"blerg-runner-session-s-oat"`) {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN must come from the per-session secret, got %s", body)
	}
	if strings.Contains(body, "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY must be absent entirely with a personal OAuth token: %s", body)
	}
}

// TestCreateSessionJobNoPersonalCredentialsFallsBack: with neither an engine
// nor a github personal credential, every key keeps coming from the operator
// Secret exactly as before.
func TestCreateSessionJobNoPersonalCredentialsFallsBack(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-fb", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-none", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 0 {
		t.Errorf("no personal credential means no per-session secret, got %d", len(f.createdSecrets))
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{
		`"key":"ANTHROPIC_API_KEY","name":"blerg-runner-agent"`,
		`"key":"CLAUDE_CODE_OAUTH_TOKEN","name":"blerg-runner-agent"`,
		`"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fallback manifest missing %s\n%s", want, body)
		}
	}
}

// TestGitURLBaseDefaultsToGitHub: with no BLERG_RUNNER_AGENT_GIT_BASE and no
// configured org, cluster sessions still have a usable base — org/name repos
// resolve against github.com itself.
func TestGitURLBaseDefaultsToGitHub(t *testing.T) {
	for _, tc := range []struct{ envBase, org, want string }{
		{"", "", "https://github.com"},
		{"", "acme", "https://github.com/acme"},
		{"https://git.example.test/team", "acme", "https://git.example.test/team"},
	} {
		if got := defaultGitURLBase(tc.envBase, tc.org); got != tc.want {
			t.Errorf("defaultGitURLBase(%q, %q) = %q, want %q", tc.envBase, tc.org, got, tc.want)
		}
	}
}

// TestGitURLForOrgSlashName: an org/name repo appends to the base without a
// doubled slash, so the default base yields https://github.com/org/name.git.
func TestGitURLForOrgSlashName(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitURLBase = "https://github.com/"
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-url", Repo: "acme/widget"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"BLERG_RUNNER_GIT_URL","value":"https://github.com/acme/widget.git"`) {
		t.Errorf("git URL wrong: %s", raw)
	}
}

// TestStatusReportsGitConfigured: the status dashboard needs to say whether an
// operator-wide git token exists at all (a user without a personal GitHub
// credential can only clone when it does).
func TestStatusReportsGitConfigured(t *testing.T) {
	withToken := &fakeK8s{secrets: map[string]map[string]string{
		"blerg-runner-agent": {"BLERG_RUNNER_GIT_TOKEN": b64("ghp_operator")},
	}}
	if got := newTestJobManager(t, withToken).Status(); !got.GitConfigured {
		t.Error("GitConfigured = false with an operator git token present")
	}
	without := &fakeK8s{secrets: map[string]map[string]string{
		"blerg-runner-agent": {"BLERG_RUNNER_GIT_TOKEN": b64("")},
	}}
	if got := newTestJobManager(t, without).Status(); got.GitConfigured {
		t.Error("GitConfigured = true with an empty operator git token")
	}
}

// TestCreateSessionJobEmptyPersonalCredentialFallsBack: core answering 200
// with an empty (or whitespace-only) plaintext must be treated exactly like a
// 404. Otherwise the empty value would suppress the operator fallback and ship
// an empty credential var into the pod — a session that fails to authenticate
// for no visible reason.
func TestCreateSessionJobEmptyPersonalCredentialFallsBack(t *testing.T) {
	for name, value := range map[string][]byte{"empty": {}, "whitespace": []byte(" \n\t")} {
		t.Run(name, func(t *testing.T) {
			f := &fakeK8s{credentialFetchResponses: map[string][]byte{
				"acct-1:claude": value,
			}}
			jm := newTestJobManager(t, f)
			if err := jm.CreateSessionJob(SessionJobSpec{
				SessionID: "s-empty", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
			}); err != nil {
				t.Fatal(err)
			}
			if len(f.createdSecrets) != 0 {
				t.Errorf("an empty personal credential must not create a per-session secret, got %d", len(f.createdSecrets))
			}
			raw, _ := json.Marshal(f.created[0])
			body := string(raw)
			for _, want := range []string{
				`"key":"ANTHROPIC_API_KEY","name":"blerg-runner-agent"`,
				`"key":"CLAUDE_CODE_OAUTH_TOKEN","name":"blerg-runner-agent"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("must fall back to the operator secret, missing %s\n%s", want, body)
				}
			}
		})
	}
}

// TestCreateSessionJobEmptyPersonalGitTokenFallsBack: same rule on the github
// branch — an empty personal git token must not shadow the operator's.
func TestCreateSessionJobEmptyPersonalGitTokenFallsBack(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-1:github": {},
	}}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-empty-gh", Repo: "org/r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 0 {
		t.Errorf("an empty personal git token must not create a per-session secret, got %d", len(f.createdSecrets))
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
		t.Errorf("must fall back to the operator git token, got %s", raw)
	}
}

// TestCreateSessionJobExtraEnvWinsOverCredentialKey: a caller-supplied ExtraEnv
// entry that collides with one of the six credential keys must produce exactly
// one env entry of that name — duplicate names in a container spec are
// resolved by k8s in a way no caller should have to reason about.
func TestCreateSessionJobExtraEnvWinsOverCredentialKey(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-dup", Repo: "org/r",
		ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "brokered"},
	}); err != nil {
		t.Fatal(err)
	}
	if n := countEnvEntries(t, f.created[0], "ANTHROPIC_API_KEY"); n != 1 {
		t.Errorf("ANTHROPIC_API_KEY appears %d times in the container env, want exactly 1", n)
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"key":"ANTHROPIC_API_KEY","name":"blerg-runner-session-s-dup"`) {
		t.Errorf("the surviving entry must be the per-session ExtraEnv one, got %s", raw)
	}
}

// countEnvEntries counts how many entries in the created Job's single
// container env list carry the given name.
func countEnvEntries(t *testing.T, job map[string]any, name string) int {
	t.Helper()
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name string `json:"name"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(parsed.Spec.Template.Spec.Containers))
	}
	n := 0
	for _, e := range parsed.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			n++
		}
	}
	return n
}

// TestCreateSessionJobAdoptsSecretIntoJob covers the leak fixed in the final
// review wave: the per-session Secret was deleted only by DeleteSessionJob, so
// every session that ended via ttlSecondsAfterFinished/activeDeadlineSeconds
// (k8s GC, which never calls back into runner) stranded a plaintext personal
// credential in the cluster forever. An ownerReferences link to the Job makes
// k8s's own cascade clean it up.
func TestCreateSessionJobAdoptsSecretIntoJob(t *testing.T) {
	f := &fakeK8s{
		jobUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-own", Repo: "r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	patch, ok := f.patchedSecrets["blerg-runner-session-s-own"]
	if !ok {
		t.Fatalf("per-session secret was never patched with ownerReferences; patched = %v", f.patchedSecrets)
	}
	raw, _ := json.Marshal(patch)
	for _, want := range []string{
		`"ownerReferences"`,
		`"kind":"Job"`,
		`"apiVersion":"batch/v1"`,
		`"name":"blerg-runner-agent-s-own"`,
		`"uid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ownerReferences patch missing %s; got %s", want, raw)
		}
	}
}

// TestCreateSessionJobNoSecretNoAdoption: sessions with no personal credential
// create no Secret, so there is nothing to patch.
func TestCreateSessionJobNoSecretNoAdoption(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-none", Repo: "r", Engine: "claude", SpawningAccountID: "acct-no-creds", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.patchedSecrets) != 0 {
		t.Errorf("patched secrets with no per-session secret: %v", f.patchedSecrets)
	}
}

// TestCreateSessionJobDeletesSecretWhenJobCreateFails: a Secret created for a
// Job that then fails to be created would otherwise strand a plaintext
// credential nothing ever looks at again.
func TestCreateSessionJobDeletesSecretWhenJobCreateFails(t *testing.T) {
	f := &fakeK8s{
		failNext: true,
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-fail", Repo: "r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	})
	if err == nil {
		t.Fatal("CreateSessionJob = nil, want the job-create failure")
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected the per-session secret to have been created first, got %d", len(f.createdSecrets))
	}
	found := false
	for _, name := range f.deletedSecrets {
		if name == "blerg-runner-session-s-fail" {
			found = true
		}
	}
	if !found {
		t.Errorf("orphaned per-session secret was not deleted; deletedSecrets = %v", f.deletedSecrets)
	}
}

func TestCreateSessionJobFallsBackToSharedSecretWithNoPersonalCredential(t *testing.T) {
	f := &fakeK8s{} // no credentialFetchResponses configured
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s2", Repo: "r", Engine: "claude", SpawningAccountID: "acct-no-creds", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 0 {
		t.Error("expected no per-session secret when the account has no personal credential")
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"key":"CLAUDE_CODE_OAUTH_TOKEN","name":"blerg-runner-agent"`) {
		t.Errorf("must fall back to the shared secret for CLAUDE_CODE_OAUTH_TOKEN, got %s", raw)
	}
}

func TestDeleteSessionJobDeletesPerSessionSecret(t *testing.T) {
	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s3", Repo: "r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := jm.DeleteSessionJob("s3"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range f.deletedSecrets {
		if name == "blerg-runner-session-s3" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected blerg-runner-session-s3 to be deleted, got %v", f.deletedSecrets)
	}
}

func TestAvailableEnginesFromSecret(t *testing.T) {
	f := &fakeK8s{secrets: map[string]map[string]string{
		"blerg-runner-agent": {
			"ANTHROPIC_API_KEY":         b64("sk-ant-test"),
			"CODEX_AUTH_JSON":           b64(`{"token":"x"}`),
			"HERMES_ENV_CONTENTS":       b64(""), // present but empty — must not count
			"BLERG_RUNNER_DAEMON_TOKEN": b64("tok"),
		},
	}}
	jm := newTestJobManager(t, f)
	got := jm.AvailableEngines()
	want := map[string]bool{"claude": true, "codex": true}
	if len(got) != len(want) {
		t.Fatalf("AvailableEngines() = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected engine %q in %v", id, got)
		}
	}
}

func TestAvailableEnginesMissingSecret(t *testing.T) {
	f := &fakeK8s{} // no secrets configured at all
	jm := newTestJobManager(t, f)
	if got := jm.AvailableEngines(); len(got) != 0 {
		t.Errorf("AvailableEngines() = %v, want empty with no Secret", got)
	}
}

func TestAvailableEnginesCaching(t *testing.T) {
	f := &fakeK8s{secrets: map[string]map[string]string{
		"blerg-runner-agent": {"ANTHROPIC_API_KEY": b64("sk-ant-test")},
	}}
	jm := newTestJobManager(t, f)
	first := jm.AvailableEngines()
	if len(first) != 1 || first[0] != "claude" {
		t.Fatalf("first call = %v, want [claude]", first)
	}
	// Mutate the backing secret store directly — AvailableEngines should
	// still return the cached value within clusterSecretsCacheTTL.
	f.mu.Lock()
	f.secrets["blerg-runner-agent"] = map[string]string{}
	f.mu.Unlock()
	second := jm.AvailableEngines()
	if len(second) != 1 || second[0] != "claude" {
		t.Errorf("cached call = %v, want unchanged [claude]", second)
	}
}

func TestJobManagerStatus(t *testing.T) {
	f := &fakeK8s{active: 1, secrets: map[string]map[string]string{
		"blerg-runner-agent": {"HERMES_ENV_CONTENTS": b64("OPENROUTER_API_KEY=x")},
	}}
	jm := newTestJobManager(t, f)
	status := jm.Status()
	if !status.Configured {
		t.Error("Status().Configured = false, want true")
	}
	if status.ActiveSessions != 1 {
		t.Errorf("ActiveSessions = %d, want 1", status.ActiveSessions)
	}
	if status.DaemonID != clusterDaemonID {
		t.Errorf("DaemonID = %q, want %q", status.DaemonID, clusterDaemonID)
	}
	if len(status.AvailableEngines) != 1 || status.AvailableEngines[0] != "hermes" {
		t.Errorf("AvailableEngines = %v, want [hermes]", status.AvailableEngines)
	}
	if status.Namespace != jm.Namespace || status.Image != jm.Image {
		t.Error("Status() didn't carry through Namespace/Image")
	}
}

// TestCreateSessionJobConflictLiveJobCleansUpSecret covers M7: a per-session
// Secret created for this call's Job (here, for a personal credential) must
// not be stranded when the Job POST 409s against an already-live Job — that
// live Job is not this call's Job and will never adopt the Secret via
// ownerReferences.
func TestCreateSessionJobConflictLiveJobCleansUpSecret(t *testing.T) {
	f := &fakeK8s{
		conflictJob: true,
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-conflict", Repo: "r", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	})
	if err != nil {
		t.Fatalf("CreateSessionJob (live-Job 409) = %v, want nil", err)
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected the per-session secret to have been created before the 409, got %d", len(f.createdSecrets))
	}
	found := false
	for _, name := range f.deletedSecrets {
		if name == "blerg-runner-session-s-conflict" {
			found = true
		}
	}
	if !found {
		t.Errorf("secret created before a live-Job 409 must be cleaned up; deletedSecrets = %v", f.deletedSecrets)
	}
}

// TestCreateSessionJobExtraEnvViaSecret covers M6: ExtraEnv (runner-brokered
// session tokens, e.g. BLERG_BOARD_TOKEN) must never appear as a literal
// value in the Job spec — visible via `kubectl describe job`/`get job -o
// yaml` to anyone with Job read access but not Secret read access. It must
// be a secretKeyRef into the per-session Secret instead.
func TestCreateSessionJobExtraEnvViaSecret(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-extra", Repo: "r",
		ExtraEnv: map[string]string{"BLERG_BOARD_TOKEN": "t"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected a per-session secret to carry ExtraEnv, got %d", len(f.createdSecrets))
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), `"name":"blerg-runner-session-s-extra"`) {
		t.Errorf("per-session secret must be named blerg-runner-session-s-extra, got %s", secretRaw)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if strings.Contains(body, `"BLERG_BOARD_TOKEN","value":"t"`) {
		t.Errorf("BLERG_BOARD_TOKEN must never appear as a literal env value in the Job spec: %s", body)
	}
	if !strings.Contains(body, `"key":"BLERG_BOARD_TOKEN","name":"blerg-runner-session-s-extra"`) {
		t.Errorf("BLERG_BOARD_TOKEN must be a secretKeyRef into the per-session secret, got %s", body)
	}
}

// TestResumeClusterSessionPassesSpawningAccountID covers M1: a resumed
// cluster session must keep using the spawning account's personal
// credential (via blerg-core) rather than silently falling back to the
// shared operator Secret — resumeClusterSession must read
// spawning_account_id back from the sessions row and pass it through.
// Requires TEST_DATABASE_URL; skips otherwise.
func TestResumeClusterSessionPassesSpawningAccountID(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000f1"
	sessionID := "00000000-0000-0000-0000-0000000000f2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected",
		"/workspace/proj", "proj", "Resume Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := db.SetSessionSpawningAccount(ctx, pool, sessionID, "acct-1"); err != nil {
		t.Fatalf("SetSessionSpawningAccount: %v", err)
	}
	if err := db.SetSessionEngine(ctx, pool, sessionID, "claude"); err != nil {
		t.Fatalf("SetSessionEngine: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE sessions SET status = 'disconnected' WHERE id = $1", sessionID); err != nil {
		t.Fatalf("set status disconnected: %v", err)
	}

	f := &fakeK8s{
		credentialFetchResponses: map[string][]byte{
			"acct-1:claude": []byte("sk-ant-personal-token"),
		},
	}
	jm := newTestJobManager(t, f)
	hub := NewHub()
	hub.SetJobManager(jm)

	resumeClusterSession(ctx, hub, pool, sessionID, "continue please", "acct-1", testSID)

	f.mu.Lock()
	calls := f.credentialFetchCalls
	f.mu.Unlock()
	if calls == 0 {
		t.Fatal("resumeClusterSession never called the core credential-fetch endpoint — SpawningAccountID not passed through")
	}
	// The launcher's own resume is authorised by the resuming browser's session.
	f.mu.Lock()
	for i, sid := range f.credentialFetchSessionIDs {
		if sid != testSID || f.credentialFetchTokenIDs[i] != "" {
			t.Errorf("resume fetch %d carried session_id %q token_id %q, want the requester's session only", i, sid, f.credentialFetchTokenIDs[i])
		}
	}
	f.mu.Unlock()
	if len(f.createdSecrets) != 1 {
		t.Fatalf("expected the resumed session's personal credential to land in a per-session secret, got %d", len(f.createdSecrets))
	}
	secretRaw, _ := json.Marshal(f.createdSecrets[0])
	if !strings.Contains(string(secretRaw), fmt.Sprintf(`"name":"blerg-runner-session-%s"`, sessionID)) {
		t.Errorf("per-session secret name mismatch: %s", secretRaw)
	}
}

// TestCreateSessionJobSecretFailureHidesK8sBody covers M1: CreateSessionJob's
// error reaches the browser through the spawn endpoint, and the k8s API's
// rejection body can echo back the request it rejected — i.e. the very
// credential material the Secret carries. The caller must get one fixed
// string, with the detail left in the server log.
func TestCreateSessionJobSecretFailureHidesK8sBody(t *testing.T) {
	const sentinel = "SENTINEL-sk-ant-oat-leaked-value"
	f := &fakeK8s{
		secretCreateStatus: http.StatusUnprocessableEntity,
		secretCreateBody: `{"kind":"Status","message":"Secret in version \"v1\" cannot be handled: ` +
			sentinel + `","reason":"Invalid","code":422}`,
	}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-leak", Repo: "r", InitialPrompt: "build it",
	})
	if err == nil {
		t.Fatal("a Secret-create failure must fail the spawn, not degrade to a literal env value")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("the k8s response body must never reach the caller: %v", err)
	}
	if strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "Status") {
		t.Errorf("the k8s status/detail must never reach the caller: %v", err)
	}
	if err.Error() != "could not prepare session credentials" {
		t.Errorf("error = %q, want the fixed %q", err, "could not prepare session credentials")
	}
	if len(f.created) != 0 {
		t.Errorf("no Job may be created when its Secret could not be: %d", len(f.created))
	}
}

// M-8: a rejected Job create is the same shape of leak as a rejected Secret.
// The k8s message echoes the manifest it refused — the session's env, image
// and namespace — and this error becomes both the start's 503 body and the
// session's stored error_reason, so the caller gets one fixed string.
func TestCreateSessionJobFailureHidesK8sBody(t *testing.T) {
	const sentinel = "SENTINEL-namespace-and-env-detail"
	f := &fakeK8s{
		failNext:      true,
		jobCreateBody: `{"kind":"Status","message":"Job is invalid: ` + sentinel + `","code":500}`,
	}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s-joberr", Repo: "r", InitialPrompt: "build it"})
	if err == nil {
		t.Fatal("a rejected Job create must fail the spawn")
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "Status") ||
		strings.Contains(err.Error(), "500") {
		t.Errorf("the k8s response body must never reach the caller: %v", err)
	}
	if err.Error() != "could not create session job" {
		t.Errorf("error = %q, want the fixed %q", err, "could not create session job")
	}
}

// TestResumeClusterSessionDoesNotReMintAnotherUsersCredentials covers I1: the
// stored spawning_account_id is a record of who launched the session, not an
// authorization to act as them. A resume requested by a different signed-in
// account — or by the board/runner-key path, which carries no human identity
// at all — must fall back to the shared operator Secret rather than fetching
// (and shipping into a pod) the launcher's personal Claude and GitHub tokens.
// Requires TEST_DATABASE_URL; skips otherwise.
func TestResumeClusterSessionDoesNotReMintAnotherUsersCredentials(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000e1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	cases := []struct {
		name      string
		sessionID string
		requester string
	}{
		// A second signed-in human sending into someone else's disconnected
		// cluster session.
		{"different user", "00000000-0000-0000-0000-0000000000e2", "acct-2"},
		// POST /runner/sessions/{id}/message — authenticated by the runner
		// key, so there is no account behind it.
		{"board runner-key path", "00000000-0000-0000-0000-0000000000e3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.InsertSession(ctx, pool, tc.sessionID, daemonID, "disconnected",
				"/workspace/proj", "proj", "Resume Test", ""); err != nil {
				t.Fatalf("InsertSession: %v", err)
			}
			if err := db.SetSessionSpawningAccount(ctx, pool, tc.sessionID, "acct-1"); err != nil {
				t.Fatalf("SetSessionSpawningAccount: %v", err)
			}
			if err := db.SetSessionEngine(ctx, pool, tc.sessionID, "claude"); err != nil {
				t.Fatalf("SetSessionEngine: %v", err)
			}
			if _, err := pool.Exec(ctx, "UPDATE sessions SET status = 'disconnected' WHERE id = $1", tc.sessionID); err != nil {
				t.Fatalf("set status disconnected: %v", err)
			}

			f := &fakeK8s{
				credentialFetchResponses: map[string][]byte{
					"acct-1:claude": []byte("sk-ant-personal-token"),
					"acct-1:github": []byte("ghp-personal-token"),
					"acct-2:claude": []byte("sk-ant-other-token"),
					"acct-2:github": []byte("ghp-other-token"),
				},
			}
			jm := newTestJobManager(t, f)
			hub := NewHub()
			hub.SetJobManager(jm)

			resumeClusterSession(ctx, hub, pool, tc.sessionID, "continue please", tc.requester, testSID)

			f.mu.Lock()
			calls := f.credentialFetchCalls
			created := len(f.created)
			secrets := append([]map[string]any(nil), f.createdSecrets...)
			f.mu.Unlock()

			if created != 1 {
				t.Fatalf("expected the resume to still create a Job, got %d", created)
			}
			if calls != 0 {
				t.Errorf("credential-fetch calls = %d, want 0: a resume by %q must not mint acct-1's personal credentials",
					calls, tc.requester)
			}
			// The prompt still needs a per-session Secret; it must carry
			// nothing but the prompt.
			for _, s := range secrets {
				raw, _ := json.Marshal(s)
				for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", gitTokenKey} {
					if strings.Contains(string(raw), key) {
						t.Errorf("per-session secret must not carry %s on a non-launcher resume: %s", key, raw)
					}
				}
			}
			// Every credential env var must come from the operator Secret.
			jobRaw, _ := json.Marshal(f.created[0])
			if !strings.Contains(string(jobRaw), `"key":"ANTHROPIC_API_KEY","name":"blerg-runner-agent"`) {
				t.Errorf("ANTHROPIC_API_KEY must fall back to the operator Secret: %s", jobRaw)
			}
		})
	}
}
