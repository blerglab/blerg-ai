package api_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/contracts/childlimits"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestChildLimitsEnvironmentSeedsDefaultsButSavedSettingsWin(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "5")
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "240")
	deps, pool := newTestDeps(t, nil, nil)
	svc := deps.Identity.(*identity.Service)
	got, err := svc.ChildLimits(t.Context())
	if err != nil || got.Set || got.Parallel != 5 || got.StartsPerHour != 240 {
		t.Fatalf("seed defaults: %+v %v", got, err)
	}
	admin := insertCredAccount(t, pool, "child-limit-admin")
	saved := childlimits.Limits{Parallel: 7, StartsPerHour: 360}
	if err := svc.SetChildLimits(t.Context(), admin, saved); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "1")
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "1")
	got, err = svc.ChildLimits(t.Context())
	if err != nil || !got.Set || got.Limits != saved {
		t.Fatalf("saved settings: %+v %v", got, err)
	}
	var by string
	if err := pool.QueryRow(t.Context(), `SELECT updated_by::text FROM child_start_limits WHERE id`).Scan(&by); err != nil || by != admin {
		t.Fatalf("actor: %q %v", by, err)
	}
}
