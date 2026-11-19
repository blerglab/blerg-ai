package runner

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/skills"
	"os"
	"testing"
)

func TestSkillPolicyPodTransport(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want skills.Policy
	}{
		{`{"test-driven-development":false,"ux-design":true,"coordinating":false}`, skills.Policy{Planning: true, UX: true, Visualizing: true}},
		{`{"test-driven-development":true,"ux-design":true,"coordinating":true}`, skills.Policy{Planning: true, TDD: true, UX: true, Coordinating: true, Visualizing: true}},
		{`broken`, skills.Policy{}},
		{``, skills.Resolve(false)},
	} {
		t.Setenv(daemon.SkillPolicyEnvVar, tc.raw)
		policy := takeSkillPolicyEnv()
		if *policy != tc.want {
			t.Fatalf("%q: %+v", tc.raw, policy)
		}
		if _, exists := os.LookupEnv(daemon.SkillPolicyEnvVar); exists {
			t.Fatal("reserved policy env leaked")
		}
		wire, err := json.Marshal(podSpawn(Config{SkillPolicy: policy}, "/workspace"))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Policy skills.Policy `json:"skill_policy"`
		}
		if json.Unmarshal(wire, &got) != nil || got.Policy != tc.want {
			t.Fatalf("pod spawn policy lost: %s", wire)
		}
	}
}
