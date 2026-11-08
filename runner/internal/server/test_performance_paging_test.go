package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTestPerformancePagingAndQuietProjectInventory(t *testing.T) {
	f := newWorkFx(t)
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(sid string, count int, at time.Time) {
		t.Helper()
		for i := 0; i < count; i++ {
			id := newUUID()
			run := performanceRun{WorkspaceID: sid, MemberID: "repository", RunID: id, Revision: 1, Source: "gate", FinishedAt: at.Add(-time.Duration(i) * time.Minute), Suites: []performanceSuite{}}
			raw, _ := json.Marshal(run)
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO test_runs(workspace_id,member_id,session_id,run_id,revision,finished_at,report) VALUES($1,'repository',$1,$2,1,$3,$4)`, sid, id, run.FinishedAt, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(f.sess, 4, now)
	seed(f.ended, 1, now.Add(-24*time.Hour))
	read := func(path string) struct {
		Runs        []performanceRun     `json:"runs"`
		Next        string               `json:"next_cursor"`
		Projects    []performanceProject `json:"projects"`
		Truncated   bool                 `json:"truncated"`
		ScanLimited bool                 `json:"scan_limited"`
	} {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
		out := httptest.NewRecorder()
		f.mux.ServeHTTP(out, req)
		var reply struct {
			Runs        []performanceRun     `json:"runs"`
			Next        string               `json:"next_cursor"`
			Projects    []performanceProject `json:"projects"`
			Truncated   bool                 `json:"truncated"`
			ScanLimited bool                 `json:"scan_limited"`
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &reply) != nil {
			t.Fatalf("read=%d %s", out.Code, out.Body.String())
		}
		return reply
	}
	page := read("/api/test-performance?limit=2")
	if len(page.Runs) != 2 || page.Next == "" || len(page.Projects) != 2 || !page.Truncated {
		t.Fatalf("first page=%+v", page)
	}
	next := read("/api/test-performance?limit=2&cursor=" + url.QueryEscape(page.Next))
	if len(next.Runs) != 2 || next.Runs[0].RunID == page.Runs[0].RunID || next.Next == "" || next.Projects != nil {
		t.Fatalf("next=%+v", next)
	}
	last := read("/api/test-performance?limit=2&cursor=" + url.QueryEscape(next.Next))
	if len(last.Runs) != 1 || last.Runs[0].WorkspaceID != f.ended || last.Next != "" || last.Truncated {
		t.Fatalf("last=%+v", last)
	}
	quiet := read("/api/test-performance?workspace=" + f.ended + "&member=repository")
	if len(quiet.Runs) != 1 || quiet.Runs[0].WorkspaceID != f.ended {
		t.Fatalf("quiet=%+v", quiet)
	}
	// Moving a source under a different Main invalidates its stored root attribution.
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, f.sess, f.ended); err != nil {
		t.Fatal(err)
	}
	seed(f.sess, 804, now)
	visible := read("/api/test-performance?limit=2")
	if len(visible.Runs) != 0 || visible.Truncated || visible.Next == "" || !visible.ScanLimited {
		t.Fatalf("scope change affected page coverage: %+v", visible)
	}
	assertContinuation := func(next string) {
		t.Helper()
		continued := read("/api/test-performance?limit=2&cursor=" + url.QueryEscape(next))
		if len(continued.Runs) != 1 || continued.Runs[0].WorkspaceID != f.ended || continued.Next != "" {
			t.Fatalf("bounded scan lost quiet history: %+v", continued)
		}
	}
	assertContinuation(visible.Next)
	// The same bounded progress applies when a selected member is removed.
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=NULL WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO session_workspaces(session_id,manifest,results,launch_generation) VALUES($1,$2,'[]','fixture-generation')`, f.sess, `{"version":2,"kind":"folder","members":[{"id":"other"}]}`); err != nil {
		t.Fatal(err)
	}
	revoked := read("/api/test-performance?limit=2")
	if len(revoked.Runs) != 0 || revoked.Truncated || revoked.Next == "" || !revoked.ScanLimited {
		t.Fatalf("removed member exhausted the request instead of continuing: %+v", revoked)
	}
	assertContinuation(revoked.Next)
}
