package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentPodDownwardResourceLimits(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "resource-fixture", Repo: "proj"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	for _, want := range []string{`"name":"BLERG_RUNNER_POD_CPU_MILLICORES"`, `"resource":"limits.cpu"`, `"divisor":"1m"`, `"name":"BLERG_RUNNER_POD_MEMORY_BYTES"`, `"resource":"limits.memory"`, `"divisor":"1"`, `"containerName":"runner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing downward API %s", want)
		}
	}
}

func TestAgentPodResourceLimitsCannotBeForged(t *testing.T) {
	for _, key := range []string{"BLERG_RUNNER_POD_CPU_MILLICORES", "BLERG_RUNNER_POD_MEMORY_BYTES"} {
		f := &fakeK8s{}
		jm := newTestJobManager(t, f)
		if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "forged-resource", Repo: "proj", ExtraEnv: map[string]string{key: "99999999"}}); err == nil {
			t.Fatal("resource limit spoof accepted", key)
		}
		if len(f.created) != 0 {
			t.Fatal("forged pod submitted")
		}
	}
}
