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
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func TestClusterConsumerSelectionToPodAndChild(t *testing.T) {
	f := newPrivFixture(t)
	fake := &fakeK8s{credentialKinds: []string{"github", "gitlab", "claude"}, credentialFetchResponses: map[string][]byte{privAcctA + ":github": []byte("fixture-github-personal"), privAcctA + ":gitlab": []byte("fixture-gitlab-personal"), privAcctA + ":claude": []byte("sk-ant-fixture-personal")}, pods: map[string]map[string]any{}, jobUID: "fixture-job-uid"}
	j := newTestJobManager(t, fake)
	j.historyPool = f.pool
	j.WorkspaceStorageClass = "fixture-enforced"
	j.MaxSessions = 10
	j.ExcludedNodes = []string{"excluded-node"}
	claims := map[string]map[string]any{}
	jobPosts := map[string]int{}
	var mu sync.Mutex
	fallback := fake.handler()
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pods") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "POST" {
				var c map[string]any
				if json.NewDecoder(r.Body).Decode(&c) != nil {
					t.Error("invalid PVC")
					return
				}
				meta := c["metadata"].(map[string]any)
				name := meta["name"].(string)
				meta["uid"] = "uid-" + name
				claims[name] = c
				w.WriteHeader(201)
				return
			}
			c := claims[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]
			if c == nil {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(c)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var job struct{ Metadata struct{ Name string } }
			if json.Unmarshal(raw, &job) != nil {
				t.Error("invalid Job")
				return
			}
			mu.Lock()
			jobPosts[job.Metadata.Name]++
			duplicate := jobPosts[job.Metadata.Name] > 1
			mu.Unlock()
			// A Kubernetes-like fake: a second POST of the same name must
			// conflict, rather than silently accepting duplicate submissions.
			if duplicate {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"kind": "Status", "reason": "AlreadyExists"})
				return
			}
		}
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/jobs/") {
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			fake.mu.Lock()
			found := false
			for _, v := range fake.created {
				if v["metadata"].(map[string]any)["name"] == name {
					found = true
				}
			}
			fake.mu.Unlock()
			if !found {
				w.WriteHeader(404)
				return
			}
		}
		fallback(w, r)
	}))
	defer kube.Close()
	j.BaseURL = kube.URL
	j.Client = kube.Client()
	f.hub.SetJobManager(j)
	f.api.ConfigureClusterWorkspaces()
	caps := f.api.workspaceCluster.Capabilities()
	if !caps.Supports("cluster", true) || caps.LinuxHost || caps.MacOSHost || caps.Sandbox {
		t.Fatal("production cluster consumer must advertise workspace and shell", caps)
	}
	catalog := NewRepoLister("org", "", f.hub)
	catalog.cachedAt = time.Now()
	catalog.cached = []RepoInfo{{Name: "same", FullName: "org/api", Provider: "github"}, {Name: "same", FullName: "org/web", Provider: "gitlab"}}
	f.api.repos = catalog
	token := f.tok(t, "human", privAcctA, "session.start")
	post := func(selection protocol.WorkspaceSelectionRequest) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"runtime": "cluster", "kind": "agent", "engine": "claude", "selection": selection})
		r := httptest.NewRequest("POST", "/api/sessions", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.api.HandlePostSessions(w, r)
		return w
	}
	selection := protocol.WorkspaceSelectionRequest{Hosted: []protocol.HostedSelectionRef{{Provider: "github", FullName: "org/api"}, {Provider: "gitlab", FullName: "org/web"}}}
	// Standard storage exposes the same cluster capabilities without a class.
	j.WorkspaceStorageClass = ""
	if got := f.api.workspaceCluster.Capabilities(); !got.Supports("cluster", true) {
		t.Fatal("standard storage did not advertise cluster", got)
	}
	j.WorkspaceStorageClass = "fixture-enforced"
	concrete := f.api.workspaceCluster.(*clusterConsumer)
	w := post(selection)
	if w.Code != 202 {
		t.Fatalf("concrete selection: %d %s", w.Code, w.Body.String())
	}
	var response map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	id := response["session_id"]
	saved, err := db.GetSessionWorkspace(t.Context(), f.pool, id)
	if err != nil || saved == nil || saved.Manifest == nil {
		t.Fatal("manifest not persisted", err)
	}
	if saved.Manifest.Root.Inode != 0 || saved.Manifest.OwnerID != privAcctA || saved.Manifest.Digest != workspaceselection.Digest(*saved.Manifest) {
		t.Fatal("invented root or lost immutable owner/selection")
	}
	if !db.GetSessionNoOperatorFallback(t.Context(), f.pool, id) {
		t.Fatal("resume could regain operator fallback")
	}
	fake.mu.Lock()
	secrets := append([]map[string]any(nil), fake.createdSecrets...)
	jobs := append([]map[string]any(nil), fake.created...)
	fake.mu.Unlock()
	if len(jobs) != 1 || len(secrets) != 1 {
		t.Fatal("selection did not reach pod launch")
	}
	var plan clusterworkspace.Plan
	data := secrets[0]["stringData"].(map[string]any)
	if json.Unmarshal([]byte(data[clusterworkspace.Env].(string)), &plan) != nil || plan.Validate() != nil || len(plan.Members) != 2 {
		t.Fatal("P1 selection incompatible with real P3 plan")
	}
	generation, _, err := db.GetWorkspaceRuntime(t.Context(), f.pool, id)
	if err != nil || plan.Runtime.Generation != generation || plan.Runtime.LaunchGeneration != saved.Manifest.LaunchGeneration {
		t.Fatal("Job plan does not belong to its fenced generation", err)
	}
	for i, c := range plan.Members {
		if c.ID != saved.Manifest.Members[i].ID || !c.PushAuthorized || c.Token == "" || saved.Manifest.Members[i].Path != []string{"same", "same-2"}[i] {
			t.Fatal("member context lost authorization or order")
		}
	}
	if plan.Members[0].Token == plan.Members[1].Token || plan.Members[0].CredentialHost == plan.Members[1].CredentialHost {
		t.Fatal("credential contexts collapsed")
	}
	var stored []byte
	if err = f.pool.QueryRow(t.Context(), `SELECT contexts FROM cluster_workspace_storage WHERE session_id=$1`, id).Scan(&stored); err != nil || bytes.Contains(stored, []byte("fixture-github-personal")) || bytes.Contains(stored, []byte("fixture-gitlab-personal")) {
		t.Fatal("durable credential leak", err)
	}
	encoded, _ := json.Marshal(jobs[0])
	if bytes.Contains(encoded, []byte("fixture-github-personal")) || !bytes.Contains(encoded, []byte(`"mountPath":"/run/blerg-workspace-private"`)) {
		t.Fatal("pod lacks protected storage or leaks credentials")
	}
	child := *saved.Manifest
	child.Members = append([]protocol.WorkspaceMember(nil), child.Members[1:]...)
	child.Kind = "repo"
	child.LaunchGeneration = newUUID()
	child.Digest = workspaceselection.Digest(child)
	childID := newUUID()
	origin := sessionOriginFor(privAcctA, false, "")
	origin.ParentSessionID = id
	if err = db.InsertClusterSessionAs(t.Context(), f.pool, childID, clusterDaemonID, "starting", clusterworkspace.PublicRoot, "", "child", "", origin); err != nil {
		t.Fatal(err)
	}
	if err = db.InsertSessionWorkspace(t.Context(), f.pool, childID, child); err != nil {
		t.Fatal(err)
	}
	spec := SessionJobSpec{SessionID: childID, SpawningAccountID: privAcctA, AuthSessionID: testSID, Engine: "claude", ExtraEnv: withClusterSessionToken(t.Context(), f.pool, childID, nil), PluginsResolved: true}
	if err = concrete.StartChild(t.Context(), *saved.Manifest, child, spec); err != nil {
		t.Fatal("concrete child:", err)
	}
	fake.mu.Lock()
	last := fake.createdSecrets[len(fake.createdSecrets)-1]
	fake.mu.Unlock()
	if json.Unmarshal([]byte(last["stringData"].(map[string]any)[clusterworkspace.Env].(string)), &plan) != nil || len(plan.Members) != 1 || plan.Members[0].ID != child.Members[0].ID || plan.SessionID != childID {
		t.Fatal("child did not reach independent subset pod")
	}
	generation, _, err = db.GetWorkspaceRuntime(t.Context(), f.pool, childID)
	if err != nil || plan.Runtime.Generation != generation || plan.Runtime.LaunchGeneration != child.LaunchGeneration {
		t.Fatal("child Job plan does not belong to its fenced generation", err)
	}
	row, err := db.GetSession(t.Context(), f.pool, childID)
	if err != nil || row.ProjectPath != clusterworkspace.PublicRoot+"/same-2" {
		t.Errorf("child cwd uses ID rather than immutable projected path: %+v %v", row, err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal("child lost original layout context", err)
	}
	mu.Lock()
	if len(jobPosts) != 2 || jobPosts[j.jobName(id)] != 1 || jobPosts[j.jobName(childID)] != 1 {
		t.Errorf("each fenced parent/child launch must POST exactly once: %v", jobPosts)
	}
	mu.Unlock()
	single := selection
	single.Hosted = append([]protocol.HostedSelectionRef(nil), selection.Hosted[:1]...)
	if singleResponse := post(single); singleResponse.Code != 202 {
		t.Fatal("single cluster launch failed", singleResponse.Code, singleResponse.Body.String())
	} else {
		var result map[string]string
		_ = json.Unmarshal(singleResponse.Body.Bytes(), &result)
		row, err := db.GetSession(t.Context(), f.pool, result["session_id"])
		if err != nil || row.ProjectPath != clusterworkspace.PublicRoot+"/same" {
			t.Errorf("single cluster cwd is not member directory: %+v %v", row, err)
		}
	}
	selection.Hosted[0].FullName = "foreign/private"
	if w = post(selection); w.Code != 409 {
		t.Fatal("unlisted source admitted")
	}
	selection.Hosted[0].FullName = "org/api"
	j.repositoryRead = func(context.Context, string, string) error { return errors.New("fixture refusal") }
	mu.Lock()
	before := len(claims)
	mu.Unlock()
	if w = post(selection); w.Code != 409 {
		t.Fatalf("precheck failure: %d", w.Code)
	}
	mu.Lock()
	after := len(claims)
	mu.Unlock()
	if after != before {
		t.Fatal("allocated before all member prechecks")
	}
}
