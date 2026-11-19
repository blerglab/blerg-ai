package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTestPerformanceHistoryImport(t *testing.T) {
	f := newWorkFx(t)
	now := time.Now().UTC().Add(-time.Minute)
	body := map[string]any{"member_id": "repository", "revision": 1, "record": map[string]any{"id": newUUID(), "commit": fixtureSHA, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "host": "private-canary", "suites": []any{map[string]any{"suite": "runner-unit", "seconds": 30, "outcome": "pass", "log": "private-canary"}}}}
	code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body)
	if code != 201 {
		t.Fatalf("history import = %d, want 201", code)
	}
	if code, _ = f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 200 {
		t.Fatalf("retry = %d", code)
	}
	if code, _ = f.postWork("test-runs/import", f.privSess, f.sessTok, body); code != 403 {
		t.Fatalf("foreign import = %d", code)
	}
	var raw string
	if err := f.pool.QueryRow(t.Context(), `SELECT report::text FROM test_runs WHERE session_id=$1`, f.sess).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "private-canary") {
		t.Fatal("private metadata persisted")
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/test-performance?workspace="+f.sess, nil)
	req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "runner-unit") {
		t.Fatalf("owner read=%d: %s", rec.Code, rec.Body.String())
	}
	record := body["record"].(map[string]any)
	record["seconds"] = 61
	if code, _ = f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 409 {
		t.Fatalf("same-revision correction=%d", code)
	}
	body["revision"] = 2
	if code, _ = f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 201 {
		t.Fatalf("new correction=%d", code)
	}
	if saved["member_id"] != "repository" {
		t.Fatal(saved)
	}
	body["member_id"] = "foreign"
	if code, _ = f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 400 {
		t.Fatalf("unselected member = %d", code)
	}
}

func TestTestPerformanceReadOwnerOnly(t *testing.T) {
	f := newInsightsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/test-performance?workspace="+f.ids.bobB, nil)
	req.Header.Set("Authorization", "Bearer "+f.mint("alice", "session.start"))
	out := httptest.NewRecorder()
	// Exercise the registered shipped route, including its authorization boundary.
	mux := http.NewServeMux()
	f.api.RegisterWorklistRoutes(mux)
	mux.ServeHTTP(out, req)
	if out.Code != 404 {
		t.Fatalf("foreign workspace = %d", out.Code)
	}
}

func TestTestPerformanceUnknownCountsStayUnknown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	record := map[string]any{"id": newUUID(), "commit": fixtureSHA, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "suites": []any{map[string]any{"suite": "unit", "seconds": 20, "outcome": "pass", "tests": map[string]any{}}}}
	raw, _ := json.Marshal(record)
	run, ok := projectGateRecord(raw, "repository", 1, now)
	if !ok || run.Suites[0].Counts != nil {
		t.Fatalf("missing counts became measured zero: %+v", run)
	}
}

func TestTestPerformanceRetentionAndSlowestCap(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := make([]any, 55)
	for i := range tests {
		tests[i] = map[string]any{"test": "example-test", "package": "example/package", "seconds": i, "result": "pass"}
	}
	record := map[string]any{"id": newUUID(), "commit": fixtureSHA, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "changed": "01234567", "suites": []any{map[string]any{"suite": "unit", "seconds": 20, "outcome": "pass", "tests": map[string]any{"slowest": tests, "counts": map[string]any{"pass": 55, "fail": 0, "skip": 0}}}}}
	raw, _ := json.Marshal(record)
	run, ok := projectGateRecord(raw, "repository", 1, now)
	if !ok || len(run.Suites[0].Slowest) != 50 || run.Suites[0].Slowest[0].Seconds != 54 || run.Region != nil {
		t.Fatalf("incorrect projection: %+v", run)
	}
	record["finished_at"] = now.AddDate(0, 0, -181).Format(time.RFC3339)
	record["started_at"] = now.AddDate(0, 0, -181).Add(-time.Minute).Format(time.RFC3339)
	raw, _ = json.Marshal(record)
	if _, ok = projectGateRecord(raw, "repository", 1, now); ok {
		t.Fatal("expired run admitted")
	}
}

func TestTestPerformanceSelectedMembership(t *testing.T) {
	f := newWorkFx(t)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO session_workspaces(session_id,manifest,results,launch_generation) VALUES($1,$2,'[]','fixture-generation')`, f.sess, `{"version":2,"kind":"folder","members":[{"id":"selected"}]}`); err != nil {
		t.Fatal(err)
	}
	selected := func(member string) bool {
		ok, err := f.api.performanceMember(t.Context(), f.sess, f.sess, member)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !selected("selected") || selected("repository") || selected("foreign") {
		t.Fatal("member selection not enforced")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_workspaces SET manifest='{"version":2,"kind":"folder","members":[]}' WHERE session_id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if selected("selected") {
		t.Fatal("revoked member remained visible")
	}
}

func TestTestPerformanceMissingDurationIsNotZero(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	raw := json.RawMessage(`{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","commit":"0123456789abcdef0123456789abcdef01234567","started_at":"` + now + `","finished_at":"` + now + `","seconds":0,"complete":true,"suites":[{"suite":"unit","outcome":"pass"}]}`)
	if _, ok := projectGateRecord(raw, "repository", 1, time.Now().UTC()); ok {
		t.Fatal("missing suite duration became measured zero")
	}
}

func TestTestPerformanceStorageRetention(t *testing.T) {
	f := newWorkFx(t)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO test_runs(workspace_id,member_id,session_id,run_id,revision,finished_at,report) SELECT $1,'repository',$1,gen_random_uuid(),1,clock_timestamp()-n*interval '1 minute','{}'::jsonb FROM generate_series(1,2001) n`, f.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO test_runs(workspace_id,member_id,session_id,run_id,revision,finished_at,report) VALUES($1,'repository',$1,gen_random_uuid(),1,clock_timestamp()-interval '181 days','{}')`, f.sess); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	id := newUUID()
	body := map[string]any{"member_id": "repository", "revision": 1, "record": map[string]any{"id": id, "commit": fixtureSHA, "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "suites": []any{}}}
	if code, _ := f.postWork("test-runs/import", f.sess, f.sessTok, body); code != 201 {
		t.Fatalf("import=%d", code)
	}
	var count, expired, latest int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE finished_at<clock_timestamp()-interval '180 days'),count(*) FILTER (WHERE run_id=$2) FROM test_runs WHERE workspace_id=$1`, f.sess, id).Scan(&count, &expired, &latest); err != nil {
		t.Fatal(err)
	}
	if count != 2000 || expired != 0 || latest != 1 {
		t.Fatalf("retained=%d expired=%d newest=%d", count, expired, latest)
	}
	older := now.Add(-7 * 24 * time.Hour)
	record := body["record"].(map[string]any)
	record["id"] = newUUID()
	record["started_at"] = older.Add(-time.Minute).Format(time.RFC3339)
	record["finished_at"] = older.Format(time.RFC3339)
	code, receipt := f.postWork("test-runs/import", f.sess, f.sessTok, body)
	if code != 201 || receipt["status"] != "not_retained" {
		t.Fatalf("evicted import=%d %v", code, receipt)
	}

}

func TestTestPerformanceSuiteBudgetProjection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	suite := map[string]any{"suite": "unit", "seconds": 20, "outcome": "pass", "layer": "2", "budget_seconds": 10}
	record := map[string]any{"id": newUUID(), "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "seconds": 60, "complete": true, "suites": []any{suite}}
	raw, _ := json.Marshal(record)
	run, ok := projectGateRecord(raw, "repository", 1, now)
	projected, _ := json.Marshal(run)
	if !ok || !bytes.Contains(projected, []byte(`"budget_seconds":10`)) || !bytes.Contains(projected, []byte(`"layer":"2"`)) {
		t.Fatalf("historical suite budget lost: %s", projected)
	}
	for _, bad := range []any{0, -1, "invalid"} {
		suite["budget_seconds"] = bad
		raw, _ = json.Marshal(record)
		if _, ok := projectGateRecord(raw, "repository", 1, now); ok {
			t.Fatal("invalid budget accepted")
		}
	}
	suite["budget_seconds"] = 10
	suite["layer"] = "unknown"
	raw, _ = json.Marshal(record)
	if _, ok := projectGateRecord(raw, "repository", 1, now); ok {
		t.Fatal("invalid layer accepted")
	}
}

func TestTestPerformanceReportedFlakes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	suite := map[string]any{"suite": "unit", "seconds": 1, "outcome": "pass", "tests": map[string]any{"counts": map[string]int{"pass": 1, "fail": 0, "skip": 0}, "flake_reruns": []any{map[string]any{"package": "tests/example", "test": "example", "outcomes": []string{"fail", "pass"}, "log": "private-canary"}}}}
	record := map[string]any{"id": newUUID(), "seconds": 1, "complete": true, "started_at": now.Add(-time.Second).Format(time.RFC3339), "finished_at": now.Format(time.RFC3339), "suites": []any{suite}}
	raw, _ := json.Marshal(record)
	r, ok := projectGateRecord(raw, "repository", 1, now)
	encoded, _ := json.Marshal(r)
	if !ok || !bytes.Contains(encoded, []byte(`"flakes":1`)) || bytes.Contains(encoded, []byte("private-canary")) {
		t.Fatalf("flake evidence lost or unsafe: %s", encoded)
	}
}
