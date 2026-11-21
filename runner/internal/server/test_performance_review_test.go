package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTestPerformanceCommitlessAndSuperseded(t *testing.T) {
	f := newWorkFx(t)
	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{"member_id": "repository", "revision": 2, "record": map[string]any{"id": newUUID(), "commit": nil, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "suites": []any{}}}
	if code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 201 {
		t.Fatalf("commitless=%d", code)
	}
	body["revision"] = 1
	code, reply := f.postWork("test-runs/import", f.sess, f.sessTok, body)
	if code != 200 || reply["superseded"] != true {
		t.Fatalf("stale=%d %v", code, reply)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/test-performance?workspace="+f.sess, nil)
	req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
	out := httptest.NewRecorder()
	f.mux.ServeHTTP(out, req)
	var response struct {
		Runs []map[string]any `json:"runs"`
	}
	_ = json.Unmarshal(out.Body.Bytes(), &response)
	if out.Code != 200 || len(response.Runs) != 1 || response.Runs[0]["commit"] != nil {
		t.Fatalf("read=%d %s", out.Code, out.Body.String())
	}
}

func TestTestPerformanceCustomReportMetadata(t *testing.T) {
	f := newWorkFx(t)
	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{"member_id": "repository", "revision": 1, "source": "custom", "region": "internal/server", "cached_packages": 1, "record": map[string]any{"id": newUUID(), "commit": nil, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": false, "suites": []any{}}}
	if code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 201 {
		t.Fatalf("custom import=%d", code)
	}
	var stored performanceRun
	var raw []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT report FROM test_runs WHERE session_id=$1`, f.sess).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Source != "custom" || stored.Region == nil || *stored.Region != "internal/server" || stored.CachedPackages == nil || *stored.CachedPackages != 1 || stored.Complete {
		t.Fatalf("report metadata lost: %+v", stored)
	}
	for _, invalid := range []struct{ source, region string }{{"other", "internal/server"}, {"custom", "/private-canary"}, {"custom", "../outside"}, {"gate", "internal/server"}} {
		body["source"], body["region"] = invalid.source, invalid.region
		if code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 400 {
			t.Fatalf("invalid metadata import=%d", code)
		}
	}
	body["source"], body["region"] = "custom", "internal/server"
	for _, cached := range []int{-1, 100001, 1} {
		body["cached_packages"] = cached
		body["record"].(map[string]any)["complete"] = true
		if code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 400 {
			t.Fatalf("invalid cache coverage=%d", code)
		}
	}
}
