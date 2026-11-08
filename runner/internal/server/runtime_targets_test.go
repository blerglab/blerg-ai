package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeTargetHTTPCompatibility(t *testing.T) {
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, &fakeK8s{secrets: map[string]map[string]string{}}))
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	token := enableBrowserAuth(t, a)("session.start")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runner/launch-options", a.HandleLaunchOptions)
	mux.HandleFunc("POST /api/sessions", a.HandlePostSessions)
	mux.HandleFunc("GET /api/sessions", a.HandleGetSessions)
	mux.HandleFunc("POST /api/runner/start", a.HandleRunnerStart)
	a.SetRunnerKey("fixture-operator")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE runtime_targets SET name='Primary cluster' WHERE is_default`); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"", "cluster", "missing"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/launch-options?runtime_target_id="+target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if target == "missing" {
			if resp.StatusCode != 422 || body["error"] != "unknown runtime target" {
				t.Fatalf("unknown target: %d %+v", resp.StatusCode, body)
			}
		} else {
			if resp.StatusCode != 200 || body["runtime_target_id"] != "cluster" {
				t.Fatalf("default target: %d %+v", resp.StatusCode, body)
			}
			targets := body["targets"].([]any)
			if len(targets) != 1 || targets[0].(map[string]any)["runtime_target_id"] != "cluster" || targets[0].(map[string]any)["name"] != "Primary cluster" {
				t.Fatalf("targets: %+v", targets)
			}
		}
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions", strings.NewReader(`{"runtime":"cluster","runtime_target_id":"missing","kind":"agent","no_repo":true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 422 || body["error"] != "unknown runtime target" {
		t.Fatalf("spawn: %d %+v", resp.StatusCode, body)
	}
}

func TestRuntimeTargetHTTPLaunchPersistence(t *testing.T) {
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, &fakeK8s{}))
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	token := enableBrowserAuth(t, a)("session.start")
	a.SetRunnerKey("fixture-operator")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", a.HandlePostSessions)
	mux.HandleFunc("POST /api/runner/start", a.HandleRunnerStart)
	mux.HandleFunc("GET /api/sessions", a.HandleGetSessions)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, path := range []string{"/api/sessions", "/api/runner/start"} {
		for _, target := range []string{"", "cluster", "missing"} {
			body := map[string]any{"runtime": "cluster", "no_repo": true, "kind": "agent"}
			bearer := token
			if path == "/api/runner/start" {
				delete(body, "kind")
				bearer = "fixture-operator"
			}
			if target != "" {
				body["runtime_target_id"] = target
			}
			raw, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(raw)))
			req.Header.Set("Authorization", "Bearer "+bearer)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			err = json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if target == "missing" {
				if resp.StatusCode != 422 || result["error"] != "unknown runtime target" {
					t.Fatalf("unknown %s: %d %+v", path, resp.StatusCode, result)
				}
				continue
			}
			if resp.StatusCode != 202 {
				t.Fatalf("start %s target %q: %d %+v", path, target, resp.StatusCode, result)
			}
			id := result["session_id"].(string)
			row, err := db.GetSession(t.Context(), a.dbPool, id)
			if err != nil || row == nil || row.RuntimeTargetID == nil || *row.RuntimeTargetID != "cluster" {
				t.Fatalf("persisted %s: %+v %v", path, row, err)
			}
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sessions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 {
		t.Fatal("no sessions returned")
	}
	for _, session := range sessions {
		if session["runtime_target_id"] != "cluster" {
			t.Fatalf("session target missing: %+v", session)
		}
	}
}

func TestRuntimeTargetCronHTTP(t *testing.T) {
	f := newCronsFx(t)
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	for _, target := range []string{"missing", "cluster", ""} {
		raw := f.body(func(body map[string]any) {
			if target != "" {
				delete(body, "runtime")
				body["runtime_target_id"] = target
			}
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crons", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+f.human())
		before := len(f.minter.minted())
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if target == "missing" {
			if resp.StatusCode != 422 || body["error"] != "unknown runtime target" || len(f.minter.minted()) != before {
				t.Fatalf("unknown cron target: %d %+v", resp.StatusCode, body)
			}
			continue
		}
		wantRuntime := "auto"
		if target == "cluster" {
			wantRuntime = "cluster"
		}
		if resp.StatusCode != 201 || (target == "cluster" && body["runtime_target_id"] != "cluster") || (target == "" && body["runtime_target_id"] != nil) || body["runtime"] != wantRuntime {
			t.Fatalf("cron target %q: %d %+v", target, resp.StatusCode, body)
		}
		patch, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/crons/"+body["id"].(string), strings.NewReader(`{"runtime_target_id":""}`))
		patch.Header.Set("Authorization", "Bearer "+f.human())
		patched, err := srv.Client().Do(patch)
		if err != nil {
			t.Fatal(err)
		}
		var updated map[string]any
		err = json.NewDecoder(patched.Body).Decode(&updated)
		patched.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if patched.StatusCode != 200 || (target == "cluster" && updated["runtime_target_id"] != "cluster") || (target == "" && updated["runtime_target_id"] != nil) || updated["runtime"] != wantRuntime {
			t.Fatalf("empty cron target patch: %d %+v", patched.StatusCode, updated)
		}
	}
}

func TestRuntimeTargetIdempotencyHTTP(t *testing.T) {
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, &fakeK8s{}))
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	a.SetRunnerKey("fixture-operator")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runner/start", a.HandleRunnerStart)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	id := ""
	for _, target := range []string{"", "cluster"} {
		body := map[string]any{"runtime": "cluster", "no_repo": true}
		if target != "" {
			body["runtime_target_id"] = target
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/runner/start", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer fixture-operator")
		req.Header.Set("Idempotency-Key", "runtime-target-upgrade")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 202 {
			t.Fatalf("target %q: %d %+v", target, resp.StatusCode, result)
		}
		got := result["session_id"].(string)
		if id != "" && got != id {
			t.Fatalf("retry created another session: %s != %s", got, id)
		}
		id = got
	}
}

func TestRuntimeTargetRejectsDesktopHTTP(t *testing.T) {
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, &fakeK8s{}))
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	token := enableBrowserAuth(t, a)("session.start")
	for _, runtime := range []string{"", "docker"} {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"runtime":"`+runtime+`","runtime_target_id":"cluster","daemon_id":"fixture-daemon","kind":"agent","no_repo":true}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.HandlePostSessions(w, req)
		if w.Code != 422 {
			t.Fatalf("runtime %q: %d %s", runtime, w.Code, w.Body.String())
		}
	}
}

func TestRuntimeTargetCronAutoAndPatchPin(t *testing.T) {
	f := newCronsFx(t)
	f.hub.SetJobManager(nil)
	for _, explicit := range []bool{false, true} {
		w := f.call("POST", "/api/crons", f.human(), f.body(func(b map[string]any) {
			if explicit {
				b["runtime_target_id"] = "cluster"
			}
		}))
		if w.Code != 201 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if explicit && body["runtime"] != "cluster" {
			t.Fatalf("auto target did not pin: %+v", body)
		}
		if !explicit && (body["runtime"] != "auto" || body["runtime_target_id"] != nil) {
			t.Fatalf("unpinned auto cron received a target: %+v", body)
		}
		w = f.call("PATCH", "/api/crons/"+body["id"].(string), f.human(), `{"runtime_target_id":"cluster"}`)
		if w.Code != 200 {
			t.Fatalf("patch: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["runtime"] != "cluster" || body["runtime_target_id"] != "cluster" {
			t.Fatalf("patch did not pin: %+v", body)
		}
	}
}

func TestRuntimeTargetValidationReadsCatalog(t *testing.T) {
	h := NewHub()
	a := ownedInventoryAPI(t, h, "fixture-daemon", nil, "")
	if _, err := a.dbPool.Exec(t.Context(), `INSERT INTO runtime_targets(id,kind,name) VALUES('catalog-fixture','cluster','Fixture')`); err != nil {
		t.Fatal(err)
	}
	if err := a.runtimeTargetProblem(t.Context(), "cluster", "catalog-fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeTargetCronDesktopPatchRefused(t *testing.T) {
	f := newCronsFx(t)
	// Model legacy stored daemon rows, which the current schema no longer admits.
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE crons DROP CONSTRAINT crons_runtime_check`); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []string{"daemon", "docker"} {
		t.Run(runtime, func(t *testing.T) {
			w := f.call("POST", "/api/crons", f.human(), f.body(func(b map[string]any) { b["runtime"] = "docker" }))
			if w.Code != 201 {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			// Legacy rows can store daemon even though new forms only accept docker.
			if _, err := f.pool.Exec(t.Context(), `UPDATE crons SET runtime=$2 WHERE id=$1`, body["id"], runtime); err != nil {
				t.Fatal(err)
			}
			w = f.call("PATCH", "/api/crons/"+body["id"].(string), f.human(), `{"runtime_target_id":"cluster"}`)
			if w.Code != 422 {
				t.Fatalf("target-only patch: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRuntimeTargetCatalogUnavailable(t *testing.T) {
	a := ownedInventoryAPI(t, NewHub(), "fixture-daemon", nil, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.runtimeTargetProblem(ctx, "cluster", "cluster"); err == nil || err.Status != 503 {
		t.Fatalf("unavailable catalog: %+v", err)
	}
}
