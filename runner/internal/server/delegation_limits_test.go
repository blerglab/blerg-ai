package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/childlimits"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChildLimitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int
		warning     bool
	}{
		{"positive", "7", 7, false}, {"zero", "0", 3, true},
		{"negative", "-1", 3, true}, {"text", "invalid", 3, true},
		{"fraction", "1.5", 3, true}, {"empty", "", 3, true},
		{"overflow", "999999999999999999999999999", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", tc.value)
			t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", tc.value)
			var out bytes.Buffer
			old := log.Writer()
			log.SetOutput(&out)
			t.Cleanup(func() { log.SetOutput(old) })
			a := NewAPI(NewHub(), nil, "", nil, "")
			wantHourly := tc.want
			if tc.warning {
				wantHourly = 120
			}
			limits := a.delegationLimits(t.Context())
			if limits.PerParentRunning != tc.want || limits.PerParentStartsPerHour != wantHourly {
				t.Fatalf("limits = %+v", limits)
			}
			for _, name := range []string{"BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR"} {
				if strings.Contains(out.String(), name+" must be a positive integer between") != tc.warning {
					t.Fatalf("startup warning = %q", out.String())
				}
			}
		})
	}
}

func TestChildLimitsNoLifetimeCap(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	for range 20 {
		fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "stopped"})
	}
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("21st start"), "after-old-children")
	if code != http.StatusCreated {
		t.Fatalf("21st start: %d %v", code, out)
	}
	fx.spawn()
}

func TestChildLimitsParallelOverride(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "4")
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	for range 3 {
		fx.childOf(fx.parent)
	}
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("fourth"), "fourth")
	if code != http.StatusCreated {
		t.Fatalf("fourth start: %d %v", code, out)
	}
	fx.spawn()
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "9")
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("fifth"), "fifth")
	if code != http.StatusTooManyRequests || asString(out["error"]) != "this session already runs 4 sessions at once (the limit is 4); stop one first" {
		t.Fatalf("fifth start: %d %v", code, out)
	}
	fx.noSpawn()
}

func TestChildLimitsHourlyOverrideAndTime(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "2")
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	first := time.Now().UTC().Truncate(time.Minute).Add(-30 * time.Minute)
	for i := range 2 {
		id, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "stopped"})
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2 WHERE id=$1`, id, first.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	code, out, hdr := fx.postChild(fx.parent, fx.parentTok, claudeBody("rate limited"), "third")
	want := fmt.Sprintf("this session started 2 sessions in the last hour (the limit is 2); the next start is possible at %s", first.Add(time.Hour).Format("15:04:05 UTC"))
	if code != http.StatusTooManyRequests || asString(out["error"]) != want || hdr.Get("Retry-After") == "" {
		t.Fatalf("hourly limit: %d %v %v; want %q", code, out, hdr, want)
	}
	fx.noSpawn()
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=started_at - interval '1 hour' WHERE parent_session_id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("window freed"), "after-window")
	if code != http.StatusCreated {
		t.Fatalf("after expiry: %d %v", code, out)
	}
	fx.spawn()
}

func TestChildLimitsHonorLiveCoreSettings(t *testing.T) {
	t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "1")
	t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "1")
	fx := newDelegFx(t)
	fx.rule("claude", runnerRuntimeDocker, false)
	var mu sync.Mutex
	configured := childlimits.Limits{Parallel: 2, StartsPerHour: 5}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/child-limits" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("X-Internal-Key") != "fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(configured)
	}))
	t.Cleanup(core.Close)
	fx.api.coreURL, fx.api.coreInternalKey = core.URL, "fixture-key"
	fx.childOf(fx.parent)
	// Core's saved value overrides the env-seeded limit of one.
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, claudeBody("second"), "core-second")
	if code != http.StatusCreated {
		t.Fatalf("core override: %d %v", code, out)
	}
	fx.spawn()
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("third"), "core-third-refused")
	if code != http.StatusTooManyRequests || !strings.Contains(asString(out["error"]), "already runs 2 sessions") {
		t.Fatalf("core parallel limit: %d %v", code, out)
	}
	mu.Lock()
	configured.Parallel = 3
	mu.Unlock()
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("third"), "core-third")
	if code != http.StatusCreated {
		t.Fatalf("live update: %d %v", code, out)
	}
	fx.spawn()
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),status='stopped' WHERE parent_session_id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	configured.StartsPerHour = 3
	mu.Unlock()
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, claudeBody("hourly"), "core-hourly")
	if code != http.StatusTooManyRequests || !strings.Contains(asString(out["error"]), "started 3 sessions in the last hour") {
		t.Fatalf("live hourly update: %d %v", code, out)
	}
	mu.Lock()
	configured = childlimits.Limits{Parallel: 51, StartsPerHour: 10001}
	mu.Unlock()
	if got := fx.api.effectiveChildLimits(t.Context()); got.parallel != 3 || got.hourly != 3 {
		t.Fatalf("accepted invalid settings: %+v", got)
	}
	core.Close()
	if got := fx.api.effectiveChildLimits(t.Context()); got.parallel != 3 || got.hourly != 3 {
		t.Fatalf("lost last valid settings: %+v", got)
	}
}
