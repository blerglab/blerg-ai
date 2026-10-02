package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestJobManagerLimitsOverride(t *testing.T) {
	j := &JobManager{PodTTLSeconds: 100, PodIdleSeconds: 50}
	if ttl, idle := j.limits(); ttl != 100 || idle != 50 {
		t.Fatalf("defaults: %d %d", ttl, idle)
	}
	j.Overrides = func() map[string]int64 {
		return map[string]int64{db.SettingPodTTLSeconds: 900, db.SettingPodIdleTimeoutSeconds: 0}
	}
	if ttl, idle := j.limits(); ttl != 900 || idle != 0 {
		t.Fatalf("overrides (idle 0 = never must win): %d %d", ttl, idle)
	}
	j.Overrides = func() map[string]int64 { return nil }
	if ttl, idle := j.limits(); ttl != 100 || idle != 50 {
		t.Fatalf("unreadable overrides fall back: %d %d", ttl, idle)
	}
}

func TestEffectiveMaxSessions(t *testing.T) {
	j := &JobManager{MaxSessions: 4}
	if got := j.effectiveMaxSessions(); got != 4 {
		t.Fatalf("no overrides source: %d, want the env default 4", got)
	}
	j.Overrides = func() map[string]int64 { return map[string]int64{} }
	if got := j.effectiveMaxSessions(); got != 4 {
		t.Fatalf("absent override: %d, want 4", got)
	}
	j.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 7} }
	if got := j.effectiveMaxSessions(); got != 7 {
		t.Fatalf("override beats env: %d, want 7", got)
	}
	j.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 0} }
	if got := j.effectiveMaxSessions(); got != 4 {
		t.Fatalf("a zero override is no override: %d, want 4", got)
	}
	j.Overrides = func() map[string]int64 { return nil }
	if got := j.effectiveMaxSessions(); got != 4 {
		t.Fatalf("unreadable overrides fall back: %d, want 4", got)
	}
}

func TestMaxSessionsOverrideEnforcedByCapCheck(t *testing.T) {
	f := &fakeK8s{active: 2}
	jm := newTestJobManager(t, f) // env cap 2
	spec := SessionJobSpec{SessionID: "s", Repo: "r"}
	if err := jm.CreateSessionJob(spec); !errors.Is(err, errClusterCap) {
		t.Fatalf("at the env cap: err = %v, want the cap error", err)
	}
	jm.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 3} }
	if err := jm.CreateSessionJob(spec); err != nil {
		t.Fatalf("raised cap must allow one more pod: %v", err)
	}
	f.active = 3
	err := jm.CreateSessionJob(spec)
	if !errors.Is(err, errClusterCap) || !strings.Contains(err.Error(), "(3)") {
		t.Fatalf("at the raised cap: err = %v, want the cap error naming 3", err)
	}
	f.active = 2
	jm.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 1} }
	err = jm.CreateSessionJob(spec)
	if !errors.Is(err, errClusterCap) || !strings.Contains(err.Error(), "(1)") {
		t.Fatalf("lowered cap must refuse at the new cap: %v", err)
	}
}

func TestClusterStatusReportsEffectiveAndDefaultMaxSessions(t *testing.T) {
	jm := newTestJobManager(t, &fakeK8s{})
	if s := jm.Status(); s.MaxSessions != 2 || s.MaxSessionsDefault != 2 {
		t.Fatalf("no override: %d / %d, want 2 / 2", s.MaxSessions, s.MaxSessionsDefault)
	}
	jm.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 9} }
	if s := jm.Status(); s.MaxSessions != 9 || s.MaxSessionsDefault != 2 {
		t.Fatalf("override: %d / %d, want 9 / 2", s.MaxSessions, s.MaxSessionsDefault)
	}
}

func TestCronPreflightUsesEffectiveMaxSessions(t *testing.T) {
	ctx := context.Background()
	cf := newCronFx(t)
	jm := cf.hub.JobManager()
	cf.k8s.active = 2 // the fixture's env cap

	c := cf.newCron(nil)
	if _, err := cf.svc.Start(ctx, c, cf.run(c)); !isCapacity(err) {
		t.Fatalf("at the env cap: err = %v, want capacity", err)
	}
	jm.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 3} }
	c2 := cf.newCron(nil)
	if _, err := cf.svc.Start(ctx, c2, cf.run(c2)); isCapacity(err) {
		t.Fatalf("a raised cap must let the cron start: %v", err)
	}
	cf.k8s.active = 1
	jm.Overrides = func() map[string]int64 { return map[string]int64{db.SettingMaxSessions: 1} }
	c3 := cf.newCron(nil)
	_, err := cf.svc.Start(ctx, c3, cf.run(c3))
	if !isCapacity(err) || !strings.Contains(err.Error(), "(1)") {
		t.Fatalf("a lowered cap must hold the cron at the new cap: %v", err)
	}
}

func putClusterSettings(t *testing.T, fx *mcpFx, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/cluster/settings", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	fx.api.HandlePutClusterSettings(w, req)
	return w
}

func TestPutClusterSettingsMaxSessions(t *testing.T) {
	fx := newMCPFx(t)
	jm := fx.hub.JobManager()
	jm.Overrides = fx.api.SettingsOverrides()
	admin := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{
		Sub: mcpAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID,
		Caps: []string{"account.manage"}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	status := func(w *httptest.ResponseRecorder) (cur, def int) {
		t.Helper()
		var s struct {
			MaxSessions        int `json:"max_sessions"`
			MaxSessionsDefault int `json:"max_sessions_default"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return s.MaxSessions, s.MaxSessionsDefault
	}

	// Raise: the status reports the effective value and the env default, and an extra pod is allowed.
	fx.k8s.active = 2
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "a", Repo: "r"}); !errors.Is(err, errClusterCap) {
		t.Fatalf("before: %v", err)
	}
	w := putClusterSettings(t, fx, admin, `{"max_sessions":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	if cur, def := status(w); cur != 3 || def != 2 {
		t.Fatalf("raise status: %d / %d, want 3 / 2", cur, def)
	}
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "b", Repo: "r"}); err != nil {
		t.Fatalf("raised cap allows an extra pod: %v", err)
	}

	// Lower: refuses at the new cap.
	fx.k8s.active = 1
	if w := putClusterSettings(t, fx, admin, `{"max_sessions":1}`); w.Code != http.StatusOK {
		t.Fatalf("lower: %d %s", w.Code, w.Body.String())
	}
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "c", Repo: "r"}); !errors.Is(err, errClusterCap) {
		t.Fatalf("lowered cap refuses: %v", err)
	}

	// A field left out stays as it is.
	if w := putClusterSettings(t, fx, admin, `{"pod_ttl_seconds":7200}`); w.Code != http.StatusOK {
		t.Fatalf("ttl only: %d %s", w.Code, w.Body.String())
	} else if cur, _ := status(w); cur != 1 {
		t.Fatalf("an absent max_sessions must be unchanged: %d, want 1", cur)
	}

	// 0 and null remove the override.
	for _, body := range []string{`{"max_sessions":0}`, `{"max_sessions":null,"pod_ttl_seconds":7200}`} {
		if w := putClusterSettings(t, fx, admin, `{"max_sessions":5}`); w.Code != http.StatusOK {
			t.Fatalf("set 5: %d", w.Code)
		}
		w := putClusterSettings(t, fx, admin, body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		if cur, def := status(w); cur != 2 || def != 2 {
			t.Fatalf("%s: %d / %d, want back to the default 2 / 2", body, cur, def)
		}
	}
	fx.k8s.active = 2
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "d", Repo: "r"}); !errors.Is(err, errClusterCap) {
		t.Fatalf("default restored: %v", err)
	}

	// Bounds.
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"max_sessions":65}`, http.StatusUnprocessableEntity},
		{`{"max_sessions":-1}`, http.StatusUnprocessableEntity},
		{`{"max_sessions":"3"}`, http.StatusBadRequest},
		{`{"max_sessions":1.5}`, http.StatusBadRequest},
	} {
		w := putClusterSettings(t, fx, admin, c.body)
		if w.Code != c.want {
			t.Fatalf("%s: %d %s, want %d", c.body, w.Code, w.Body.String(), c.want)
		}
		if c.want == http.StatusUnprocessableEntity && !strings.Contains(w.Body.String(), "max_sessions must be") {
			t.Fatalf("%s: unclear message %s", c.body, w.Body.String())
		}
	}
	if cur := jm.effectiveMaxSessions(); cur != 2 {
		t.Fatalf("a rejected value must not be stored: %d", cur)
	}
	for _, body := range []string{`{"max_sessions":1}`, `{"max_sessions":64}`} {
		if w := putClusterSettings(t, fx, admin, body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := putClusterSettings(t, fx, admin, `{}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "nothing to change") {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}

	// Auth is unchanged: a non-admin is refused, no token is unauthorized, the daemon token works.
	if w := putClusterSettings(t, fx, fx.human(), `{"max_sessions":3}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin: %d, want 403", w.Code)
	}
	if w := putClusterSettings(t, fx, "", `{"max_sessions":3}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", w.Code)
	}
	if w := putClusterSettings(t, fx, "daemon-tok-1234567890", `{"max_sessions":3}`); w.Code != http.StatusOK {
		t.Fatalf("daemon token: %d %s", w.Code, w.Body.String())
	}
}
