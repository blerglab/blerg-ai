package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobSchedulingGuardsAllSubmissions(t *testing.T) {
	var received []map[string]any
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var job map[string]any
		if json.NewDecoder(r.Body).Decode(&job) != nil {
			t.Error("bad Job")
		}
		received = append(received, job)
		w.WriteHeader(201)
	}))
	defer endpoint.Close()
	j := &JobManager{Namespace: "acceptance-fixture", BaseURL: endpoint.URL, Client: endpoint.Client(), ExcludedNodes: []string{"excluded-node"}}
	for _, name := range []string{"session", "model-catalog"} {
		job := map[string]any{"kind": "Job", "metadata": map[string]any{"name": name}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"affinity": map[string]any{"podAffinity": map[string]any{"fixture": true}, "nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{}, map[string]any{"matchExpressions": []any{map[string]any{"key": "disk", "operator": "Exists"}}}}}}}}}}}
		resp, err := j.doWithContentType(context.Background(), http.MethodPost, "/apis/batch/v1/namespaces/acceptance-fixture/jobs", job, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if len(received) != 2 {
		t.Fatal("Job submission absent")
	}
	for _, job := range received {
		pod := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		affinity := pod["affinity"].(map[string]any)
		if affinity["podAffinity"] == nil {
			t.Fatal("existing affinity erased")
		}
		terms := affinity["nodeAffinity"].(map[string]any)["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)["nodeSelectorTerms"].([]any)
		if len(terms) != 2 {
			t.Fatal("required OR terms changed")
		}
		for _, raw := range terms {
			encoded, _ := json.Marshal(raw)
			s := string(encoded)
			if !strings.Contains(s, `"operator":"NotIn"`) || !strings.Contains(s, "excluded-node") || !strings.Contains(s, `"key":"kubernetes.io/hostname","operator":"Exists"`) {
				t.Fatal("SCHEDULING_EXCLUSION: required term escaped operator exclusion")
			}
		}
	}
}
func TestJobSchedulingRefusesBypassBeforeNetwork(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("scheduling refusal reached API")
		w.WriteHeader(201)
	}))
	defer endpoint.Close()
	for _, raw := range []string{`{"spec":{"template":{"spec":{"nodeName":"excluded-node"}}}}`, `{"spec":{}}`, `{"spec":{"template":{"spec":{"affinity":[]}}}}`, `{"spec":{"template":{"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[]}}}}}}}`} {
		var job map[string]any
		_ = json.Unmarshal([]byte(raw), &job)
		j := &JobManager{Namespace: "acceptance-fixture", ExcludedNodes: []string{"excluded-node"}, BaseURL: endpoint.URL, Client: endpoint.Client()}
		if _, err := j.doWithContentType(context.Background(), http.MethodPost, "/apis/batch/v1/namespaces/acceptance-fixture/jobs", job, "application/json"); err == nil {
			t.Fatal("scheduling bypass admitted")
		}
	}
	t.Setenv("BLERG_RUNNER_POD_EXCLUDE_NODES", "first-node,second-node")
	if len(excludedNodesFromEnv()) != 2 {
		t.Fatal("operator list not plumbed")
	}
}

func TestCreateSessionJobAppliesOperatorExclusion(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	j.ExcludedNodes = []string{"excluded-node"}
	if err := j.CreateSessionJob(SessionJobSpec{SessionID: "scheduling-fixture", Repo: "project"}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatal("session job absent")
	}
	raw, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(raw), `"operator":"NotIn"`) || !strings.Contains(string(raw), "excluded-node") {
		t.Fatal("SESSION_EXCLUSION: actual builder bypassed required exclusion")
	}
}
