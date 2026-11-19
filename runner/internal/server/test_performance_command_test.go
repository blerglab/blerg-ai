package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/testreportcli"
)

func TestTestPerformanceSessionReportCommand(t *testing.T) {
	f := newWorkFx(t)
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", srv.URL)
	t.Setenv("BLERG_RUNNER_SESSION_ID", f.sess)
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", f.sessTok)
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-2 * time.Second)
	goJSON := fmt.Sprintf("{\"Time\":%q,\"Action\":\"start\",\"Package\":\"example/unit\"}\n{\"Time\":%q,\"Action\":\"pass\",\"Package\":\"example/unit\",\"Test\":\"TestExample\",\"Elapsed\":1}\n{\"Time\":%q,\"Action\":\"fail\",\"Package\":\"example/unit\",\"Elapsed\":2}\n", start.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	vitestJSON := fmt.Sprintf(`{"startTime":%d,"success":false,"testResults":[{"name":"src/example.test.ts","startTime":%d,"endTime":%d,"status":"failed","assertionResults":[{"fullName":"example","status":"passed","duration":1000}]}]}`, start.UnixMilli(), start.UnixMilli(), now.UnixMilli())
	pytestJSON := fmt.Sprintf(`{"blerg_flake_reruns":[{"package":"tests/test_example.py","test":"test_example","outcomes":["fail","pass"]}],"created":%d,"duration":2,"exitcode":1,"summary":{"total":1},"tests":[{"nodeid":"tests/test_example.py::test_example","outcome":"passed","call":{"duration":1}}]}`, now.Unix())
	genericJSON := fmt.Sprintf(`{"version":1,"flake_reruns":[{"package":"tests/example","test":"example","outcomes":["fail","pass"]}],"started_at":%q,"finished_at":%q,"complete":true,"outcome":"fail","tests":[{"package":"tests/example","test":"example","outcome":"pass","seconds":1}]}`, start.Format(time.RFC3339), now.Format(time.RFC3339))
	mixed := fmt.Sprintf("{\"Time\":%q,\"Action\":\"start\",\"Package\":\"example/cached\"}\n{\"Time\":%q,\"Action\":\"output\",\"Package\":\"example/cached\",\"Output\":\"ok  \\texample/cached\\t(cached)\\n\"}\n{\"Time\":%q,\"Action\":\"pass\",\"Package\":\"example/cached\",\"Elapsed\":0}\n", start.Add(-time.Second).Format(time.RFC3339), start.Format(time.RFC3339), start.Format(time.RFC3339)) + goJSON
	for name, fixture := range map[string]struct {
		format, raw string
		cached      int
	}{"go": {"go", goJSON, 0}, "vitest": {"vitest", vitestJSON, 0}, "mixed": {"go", mixed, 1}, "pytest": {"pytest", pytestJSON, 0}, "json": {"json", genericJSON, 0}} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".blerg"), 0700); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf(`{"version":1,"suites":{%q:{"layer":"3b","budget_seconds":120}}}`, name+"-unit")
			if err := os.WriteFile(filepath.Join(root, ".blerg", "test-budgets.json"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "report.json")
			if err := os.WriteFile(path, []byte(fixture.raw), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"report", "--format", fixture.format, "--file", path, "--member", "repository", "--suite", name + "-unit", "--region", "internal/unit", "--root", root}
			for attempt := 0; attempt < 2; attempt++ {
				var out, stderr bytes.Buffer
				if code := testreportcli.Main(args, &out, &stderr); code != 0 {
					t.Fatalf("shipped report command=%d %s", code, stderr.String())
				}
			}
			var stored performanceRun
			var body []byte
			var count int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM test_runs WHERE session_id=$1 AND report->'suites'->0->>'suite'=$2`, f.sess, name+"-unit").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("default retry identity stored %d rows instead of one", count)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT report FROM test_runs WHERE session_id=$1 AND report->'suites'->0->>'suite'=$2`, f.sess, name+"-unit").Scan(&body); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Suites[0].Layer != "3b" || stored.Suites[0].BudgetSeconds == nil || *stored.Suites[0].BudgetSeconds != 120 {
				t.Fatal("stored project budget lost")
			}
			req := httptest.NewRequest(http.MethodGet, "/api/test-performance?workspace="+f.sess, nil)
			req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
			read := httptest.NewRecorder()
			f.mux.ServeHTTP(read, req)
			if read.Code != 200 || !bytes.Contains(read.Body.Bytes(), []byte(`"budget_seconds":120`)) {
				t.Fatal("project budget missing from authorized read")
			}
			if name == "pytest" || name == "json" {
				if stored.Suites[0].Flakes == nil || *stored.Suites[0].Flakes != 1 || len(stored.Suites[0].FlakeReruns) != 1 || !bytes.Contains(read.Body.Bytes(), []byte(`"flakes":1`)) || bytes.Contains(read.Body.Bytes(), []byte(`"flake_reruns"`)) {
					t.Fatal("flake storage or bounded authorized read lost")
				}
			} else if stored.Suites[0].Flakes != nil {
				t.Fatal("flake count invented without explicit evidence")
			}
			if !validSessionID(stored.RunID) {
				t.Fatal("deterministic ID is not accepted by the server")
			}
			if stored.Source != "custom" || stored.Region == nil || *stored.Region != "internal/unit" || stored.Commit != nil || len(stored.Suites) != 1 || stored.Suites[0].Seconds != 2 || stored.Suites[0].Outcome != "fail" || stored.Suites[0].Counts == nil || stored.Suites[0].Counts.Pass != 1 || len(stored.Suites[0].Slowest) != 1 {
				t.Fatalf("CLI/server boundary lost measurement: %+v", stored)
			}
			if stored.CachedPackages == nil || *stored.CachedPackages != fixture.cached || stored.Complete != (fixture.cached == 0) {
				t.Fatalf("cache coverage lost: %+v", stored)
			}
		})
	}
}
