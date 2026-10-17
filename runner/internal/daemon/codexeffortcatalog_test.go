package daemon

import (
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"testing"
)

func TestCodexKnownModelWithoutEffortsRejectsExplicitEffort(t *testing.T) {
	for _, levels := range [][]string{nil, {}} {
		em := &collectEmitter{}
		d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "fixture-original", Effort: "high", Emitter: em})
		d.modelCatalog = map[string]models.Model{"fixture-no-effort": {ID: "fixture-no-effort", Efforts: levels}}
		d.SetModel("fixture-no-effort", "high", "ui")
		if d.model != "fixture-original" || d.effort != "high" || countKind(em, "error") != 1 || countKind(em, "model_changed") != 0 {
			t.Fatalf("unsupported effort accepted: model=%q effort=%q events=%v", d.model, d.effort, em.kinds())
		}
	}
}
func TestCodexModelOnlySwitchClearsEffortWhenKnownModelSupportsNone(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "fixture-original", Effort: "high", Emitter: em})
	d.modelCatalog = map[string]models.Model{"fixture-no-effort": {ID: "fixture-no-effort"}}
	d.SetModel("fixture-no-effort", "", "ui")
	if d.model != "fixture-no-effort" || d.effort != "" || countKind(em, "model_changed") != 1 {
		t.Fatalf("model-only switch retained unsupported effort: model=%q effort=%q", d.model, d.effort)
	}
}
