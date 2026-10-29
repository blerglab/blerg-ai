package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/codexauth"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

const clusterModelFixture = `{"models":[{"slug":"fixture-fast","display_name":"Fast fixture","visibility":"list","priority":1,"default_reasoning_level":"low","supported_reasoning_levels":["low","medium","high","max","ultra"]},{"slug":"fixture-small","display_name":"Small fixture","visibility":"list","priority":2,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"max"}]},{"slug":"fixture-hidden","visibility":"hide"}]}`

type catalogFake struct {
	mu                                                sync.Mutex
	jobs, secrets, jobDeletes, secretDeletes, patches int
	phase                                             string
	exit                                              int
	logs                                              string
	rejectJob                                         bool
	rejectSecret                                      bool
	onPod                                             func()
	job                                               map[string]any
	credential                                        string
}

func (f *catalogFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/secrets/operator"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"CODEX_AUTH_JSON": base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"access_token":"fixture-private-value","refresh_token":"fixture-refresh-value"}}`))}})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/secrets"):
		f.secrets++
		if f.rejectSecret {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("fixture-private-value"))
			return
		}
		var secret struct {
			Data map[string]string `json:"stringData"`
		}
		_ = json.NewDecoder(r.Body).Decode(&secret)
		f.credential = secret.Data["auth.json"]
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs"):
		f.jobs++
		_ = json.NewDecoder(r.Body).Decode(&f.job)
		if f.rejectJob {
			w.WriteHeader(403)
			_, _ = w.Write([]byte("fixture-private-value"))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"metadata":{"uid":"fixture-job-uid"}}`))
	case r.Method == "PATCH":
		f.patches++
		_, _ = w.Write([]byte(`{}`))
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pods"):
		if f.onPod != nil {
			f.onPod()
			f.onPod = nil
		}
		_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"fixture-pod"},"status":{"phase":%q,"containerStatuses":[{"state":{"terminated":{"exitCode":%d}}}]}}]}`, f.phase, f.exit)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/log"):
		_, _ = w.Write([]byte(f.logs))
	case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/jobs/"):
		f.jobDeletes++
		_, _ = w.Write([]byte(`{}`))
	case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/secrets/"):
		f.secretDeletes++
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(404)
	}
}
func catalogManager(t *testing.T, f *catalogFake) *JobManager {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	return &JobManager{BaseURL: s.URL, Client: s.Client(), Namespace: "fixture", Image: "fixture-agent:version", SecretName: "operator"}
}

func TestClusterCodexModelsActualImageCatalogAndCache(t *testing.T) {
	f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
	j := catalogManager(t, f)
	l, err := j.CodexModels(context.Background(), "account-one", coreProof{SessionID: "session-one"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Source != models.SourceLive || len(l.Models) != 2 || l.Models[0].ID != "fixture-fast" || l.Models[0].DefaultEffort != "low" || len(l.Models[1].Efforts) != 3 {
		t.Fatalf("wrong catalog: %+v", l)
	}
	l.Models[0].ID = "mutated"
	l.Models[0].Efforts[0] = "mutated"
	second, err := j.CodexModels(context.Background(), "account-one", coreProof{SessionID: "session-one"})
	if err != nil || second.Models[0].ID != "fixture-fast" || second.Models[0].Efforts[0] != "low" {
		t.Fatalf("cache corrupted: %+v %v", second, err)
	}
	if strings.Contains(f.credential, "fixture-refresh-value") || !strings.Contains(f.credential, "fixture-private-value") {
		t.Fatal("probe credential was not access-only")
	}
	if f.jobs != 1 || f.secrets != 1 || f.jobDeletes != 1 || f.secretDeletes != 1 || f.patches != 0 {
		t.Fatalf("lifecycle: %+v", f)
	}
	raw, _ := json.Marshal(f.job)
	if strings.Contains(string(raw), "fixture-private-value") {
		t.Fatal("credential in Job manifest")
	}
	spec := f.job["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false || spec["activeDeadlineSeconds"] != float64(45) {
		t.Fatal("probe lifecycle/security missing")
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	if container["image"] != j.Image {
		t.Fatal("wrong image")
	}
	if container["securityContext"].(map[string]any)["allowPrivilegeEscalation"] != false {
		t.Fatal("probe lacks hardening")
	}
	if _, err := j.CodexModels(context.Background(), "account-two", coreProof{SessionID: "session-two"}); err != nil {
		t.Fatal(err)
	}
	if f.jobs != 2 {
		t.Fatal("catalog reused across accounts")
	}
	j.Image = "fixture-agent:new-version"
	if _, err := j.CodexModels(context.Background(), "account-two", coreProof{SessionID: "session-two"}); err != nil {
		t.Fatal(err)
	}
	if f.jobs != 3 {
		t.Fatal("catalog reused across images")
	}
}

func TestClusterCodexModelsFailuresCleanUpAndRedact(t *testing.T) {
	for _, tc := range []struct {
		name, phase, logs string
		exit              int
		reject            bool
		want              error
	}{{"login", "Failed", "fixture-private-value", 42, false, ErrClusterCodexCatalogAuth}, {"bad-json", "Succeeded", "fixture-private-value", 0, false, ErrClusterCodexCatalog}, {"too-large", "Succeeded", strings.Repeat("x", clusterCodexCatalogMaxOutput+1), 0, false, ErrClusterCodexCatalog}, {"job-rejected", "", "", 0, true, ErrClusterCodexCatalog}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &catalogFake{phase: tc.phase, exit: tc.exit, logs: tc.logs, rejectJob: tc.reject}
			j := catalogManager(t, f)
			_, err := j.CodexModels(context.Background(), "account", coreProof{SessionID: "session"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error: %v", err)
			}
			if strings.Contains(err.Error(), "fixture-private-value") {
				t.Fatal("secret in error")
			}
			wantDeletes := 1
			if tc.reject {
				wantDeletes = 0
			}
			if f.secretDeletes != wantDeletes || f.jobDeletes != wantDeletes {
				t.Fatal("probe resources leaked or pre-existing resources deleted")
			}
		})
	}
}

func TestClusterCodexModelsCoalescesRequests(t *testing.T) {
	f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
	j := catalogManager(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := j.CodexModels(context.Background(), "account", coreProof{SessionID: "session"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.jobs != 1 {
		t.Fatalf("created %d duplicate probes", f.jobs)
	}
}

func TestClusterCodexModelsRequiresLiveProof(t *testing.T) {
	f := &catalogFake{}
	j := catalogManager(t, f)
	_, err := j.CodexModels(context.Background(), "account", coreProof{})
	if !errors.Is(err, errNoLivenessProof) || f.jobs != 0 {
		t.Fatalf("missing proof accepted: %v", err)
	}
}

func TestCodexProbeAuthSnapshotWithholdsRefreshAndRejectsExpired(t *testing.T) {
	now := time.Now()
	token := func(exp int64) string {
		claims, _ := json.Marshal(map[string]int64{"exp": exp})
		return "fixture." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	}
	auth := func(exp int64) []byte {
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token(exp), "refresh_token": "fixture-refresh-value", "id_token": "fixture-id", "account_id": "fixture-account"}})
		return raw
	}
	snapshot, err := codexProbeAuthSnapshot(auth(now.Add(time.Hour).Unix()), now)
	var decoded struct {
		LastRefresh time.Time `json:"last_refresh"`
	}
	_ = json.Unmarshal(snapshot, &decoded)
	if !decoded.LastRefresh.Equal(now.UTC()) {
		t.Fatal("probe snapshot allows implicit refresh")
	}
	if err != nil || strings.Contains(string(snapshot), "refresh_token") || strings.Contains(string(snapshot), "fixture-refresh-value") || !strings.Contains(string(snapshot), "fixture-account") {
		t.Fatalf("unsafe snapshot: error=%v", err)
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`not-json`)} {
		if _, err := codexProbeAuthSnapshot(raw, now); !errors.Is(err, ErrClusterCodexCatalogAuth) {
			t.Fatalf("invalid auth accepted: %v", err)
		}
	}
	snapshot, err = codexProbeAuthSnapshot([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"fixture-api-value","tokens":{"refresh_token":"fixture-refresh-value"}}`), now)
	if err != nil || strings.Contains(string(snapshot), "refresh_token") || strings.Contains(string(snapshot), "fixture-refresh-value") {
		t.Fatal("invalid API key snapshot")
	}
}

func TestExpiredCodexSnapshotRequiresDedicatedLoginInsteadOfClaimingUnauthenticated(t *testing.T) {
	claims, _ := json.Marshal(map[string]int64{"exp": time.Now().Add(-time.Hour).Unix()})
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": "fixture." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture", "refresh_token": "fixture-shared-refresh"}})
	_, err := codexProbeAuthSnapshot(raw, time.Now())
	if err == nil || errors.Is(err, ErrClusterCodexCatalogAuth) || !strings.Contains(err.Error(), "dedicated") {
		t.Fatal("expired copied login must explain independent cluster login")
	}
}

func TestClusterCodexModelsPersonalCredentialAndExpiredAccess(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			claims, _ := json.Marshal(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()})
			if expired {
				claims, _ = json.Marshal(map[string]int64{"exp": time.Now().Add(-time.Hour).Unix()})
			}
			access := "fixture." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
			credential, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": access, "refresh_token": "fixture-personal-refresh", "account_id": "fixture-personal-account"}})
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"plaintext_base64": base64.StdEncoding.EncodeToString(credential)})
			}))
			defer core.Close()
			f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
			j := catalogManager(t, f)
			j.CoreURL = core.URL
			j.CoreInternalKey = "fixture-internal-key"
			j.CredentialClient = core.Client()
			_, err := j.CodexModels(context.Background(), "account", coreProof{SessionID: "session"})
			if expired {
				if !errors.Is(err, codexauth.ErrDedicatedLogin) || f.jobs != 0 || f.secrets != 0 {
					t.Fatal("expired access reached probe")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(f.credential, "fixture-personal-account") || strings.Contains(f.credential, "fixture-personal-refresh") {
				t.Fatal("wrong personal auth snapshot")
			}
		})
	}
}

// A model/list fallback is not proof of a remote catalog: only a cache created
// in this probe's brand-new CODEX_HOME is accepted.
func TestClusterCodexCatalogCommandRequiresFreshRemoteCache(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprint(fresh), func(t *testing.T) {
			dir := t.TempDir()
			auth := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"fixture-access"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			fake := `#!/usr/bin/python3
import json,os,pathlib,sys
native=json.loads((pathlib.Path(os.environ["CODEX_HOME"])/"auth.json").read_text())
if native.get("tokens",{}).get("refresh_token") != "": sys.exit(71)
for line in sys.stdin:
    message=json.loads(line)
    if message.get("method") not in ["initialize","initialized","model/list"]:
        sys.exit(70)
    if message.get("method")=="model/list" and os.environ.get("FIXTURE_FRESH")=="true":
        (pathlib.Path(os.environ["CODEX_HOME"])/"models_cache.json").write_text('{"identity":"fixture-private-identity","models":[{"slug":"fixture-live"}]}')
    if "id" in message:
        print(json.dumps({"id":message["id"],"result":{}}),flush=True)
`
			if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			command := strings.ReplaceAll(clusterCodexCatalogCommand, "/var/run/codex-auth/auth.json", auth)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "FIXTURE_FRESH=" + fmt.Sprint(fresh)}
			out, err := cmd.Output()
			if fresh {
				if err != nil || !strings.Contains(string(out), "fixture-live") || strings.Contains(string(out), "fixture-private-identity") {
					t.Fatalf("fresh catalog rejected: %v", err)
				}
			} else if err == nil || len(out) != 0 {
				t.Fatal("bundled fallback accepted as live catalog")
			}
		})
	}
}

func TestClusterCodexModelsSecretRejectionPreservesExistingSecret(t *testing.T) {
	f := &catalogFake{rejectSecret: true}
	j := catalogManager(t, f)
	if _, err := j.CodexModels(context.Background(), "account", coreProof{SessionID: "session"}); !errors.Is(err, ErrClusterCodexCatalog) {
		t.Fatalf("error: %v", err)
	}
	if f.jobDeletes != 1 || f.secretDeletes != 0 {
		t.Fatal("created Job leaked or rejected Secret deleted")
	}
}
func TestClusterCodexModelsCancellationCleansResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &catalogFake{phase: "Running", onPod: cancel}
	j := catalogManager(t, f)
	if _, err := j.CodexModels(ctx, "account", coreProof{SessionID: "session"}); err == nil {
		t.Fatal("cancellation succeeded")
	}
	if f.jobDeletes != 1 || f.secretDeletes != 1 {
		t.Fatal("canceled request leaked probe resources")
	}
}

func TestClusterCodexModelsForcedRefreshBypassesInnerCache(t *testing.T) {
	f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
	j := catalogManager(t, f)
	for _, force := range []bool{false, false, true, false} {
		if _, err := j.codexModels(context.Background(), "account", coreProof{SessionID: "session"}, force); err != nil {
			t.Fatal(err)
		}
	}
	if f.jobs != 2 {
		t.Fatalf("forced refresh created %d jobs, want2", f.jobs)
	}
}
