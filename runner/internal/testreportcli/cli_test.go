package testreportcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const runID = "00000000-0000-4000-8000-000000000402"
const sessionID = "00000000-0000-4000-8000-000000000001"
const goReport = `{"Time":"2026-11-01T12:00:00Z","Action":"start","Package":"example/unit"}
{"Time":"2026-11-01T12:00:01Z","Action":"pass","Package":"example/unit","Test":"TestOne","Elapsed":1}
{"Time":"2026-11-01T12:00:02Z","Action":"pass","Package":"example/unit","Elapsed":2}`

func reportFile(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func configure(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", endpoint)
	t.Setenv("BLERG_RUNNER_SESSION_ID", sessionID)
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-token")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
}

func TestReportCommandUploadsNormalizedGoReportWithRefreshedToken(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/sessions/"+sessionID+"/test-runs/import" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer refreshed-fixture" {
			t.Errorf("wrong authenticated request")
		}
		var body struct {
			Member   string `json:"member_id"`
			Source   string `json:"source"`
			Region   string `json:"region"`
			Revision int64  `json:"revision"`
			Record   struct {
				ID       string  `json:"id"`
				Commit   *string `json:"commit"`
				Seconds  float64 `json:"seconds"`
				Complete bool    `json:"complete"`
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Member != "api" || body.Source != "custom" || body.Region != "internal/unit" || body.Revision != 2 || body.Record.ID != runID || body.Record.Commit != nil || body.Record.Seconds != 2 || !body.Record.Complete {
			t.Errorf("wrong normalized report: %+v", body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true,"status":"retained"}`))
	}))
	defer srv.Close()
	configure(t, srv.URL)
	state := os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR")
	if err := os.MkdirAll(filepath.Join(state, "session-tokens"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "session-tokens", sessionID), []byte("refreshed-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Main([]string{"report", "--format", "go", "--file", reportFile(t, goReport), "--member", "api", "--suite", "go-unit", "--run-id", runID, "--revision", "2", "--region", "internal/unit"}, &out, &stderr)
	if code != 0 || !called || !strings.Contains(out.String(), runID) || !strings.Contains(out.String(), "retained") {
		t.Fatalf("report=%d called=%v out=%q stderr=%q", code, called, out.String(), stderr.String())
	}
}

func TestReportCommandPrintsPublicFailureAndWithholdsPrivateBody(t *testing.T) {
	for _, body := range []string{`{"error":"Test run revision conflicts with stored history."}`, `{"error":"private-canary"}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(409); _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			configure(t, srv.URL)
			var out, stderr bytes.Buffer
			code := Main([]string{"report", "--format", "go", "--file", reportFile(t, goReport), "--member", "api", "--suite", "go", "--run-id", runID}, &out, &stderr)
			if code == 0 || strings.Contains(stderr.String(), "private-canary") || !strings.Contains(stderr.String(), "409") {
				t.Fatalf("failure=%d %q", code, stderr.String())
			}
			if strings.Contains(body, "revision conflicts") && !strings.Contains(stderr.String(), "revision conflicts") {
				t.Fatalf("public server message dropped: %q", stderr.String())
			}
		})
	}
}

func TestReportCommandRefusesBadFlagsFilesAndReports(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(500) }))
	defer srv.Close()
	configure(t, srv.URL)
	for _, args := range [][]string{
		{"report"},
		{"report", "--format", "shell", "--file", "private-canary", "--member", "api", "--suite", "unit"},
		{"report", "--format", "go", "--file", filepath.Join(t.TempDir(), "private-canary"), "--member", "api", "--suite", "unit"},
		{"report", "--format", "go", "--file", reportFile(t, "private-canary"), "--member", "api", "--suite", "unit"},
	} {
		var out, stderr bytes.Buffer
		if code := Main(args, &out, &stderr); code == 0 || called || strings.Contains(stderr.String(), "private-canary") {
			t.Fatalf("unsafe request=%d called=%v stderr=%q", code, called, stderr.String())
		}
	}
}

func TestReportCommandHandlesReceiptsAndRejectsRedirects(t *testing.T) {
	for name, receipt := range map[string]string{"superseded": `{"ok":true,"status":"retained","superseded":true}`, "not_retained": `{"ok":true,"status":"not_retained"}`, "invalid": `{"ok":true}`} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(receipt)) }))
			defer srv.Close()
			configure(t, srv.URL)
			var out, stderr bytes.Buffer
			code := Main([]string{"report", "--format", "go", "--file", reportFile(t, goReport), "--member", "api", "--suite", "go"}, &out, &stderr)
			if name == "invalid" {
				if code == 0 {
					t.Fatal("invalid receipt accepted")
				}
				return
			}
			if code != 0 || !strings.Contains(out.String(), "Report "+name+".") {
				t.Fatalf("receipt=%d %q %q", code, out.String(), stderr.String())
			}
		})
	}
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected = true; w.WriteHeader(500) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	configure(t, srv.URL)
	var out, stderr bytes.Buffer
	if code := Main([]string{"report", "--format", "go", "--file", reportFile(t, goReport), "--member", "api", "--suite", "go"}, &out, &stderr); code == 0 || redirected || !strings.Contains(stderr.String(), "307") {
		t.Fatalf("redirect followed: %d %v %q", code, redirected, stderr.String())
	}
}

func TestReportCommandDefaultIDIsStableForRetries(t *testing.T) {
	lastID := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Record struct {
				ID string `json:"id"`
			} `json:"record"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		lastID = body.Record.ID
		_, _ = w.Write([]byte(`{"ok":true,"status":"retained"}`))
	}))
	defer srv.Close()
	configure(t, srv.URL)
	report := func(raw, member, suite string, extra ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		args := append([]string{"report", "--format", "go", "--file", reportFile(t, raw), "--member", member, "--suite", suite}, extra...)
		if code := Main(args, &out, &stderr); code != 0 {
			t.Fatalf("report=%d %q", code, stderr.String())
		}
		return lastID
	}
	first := report(goReport, "api", "unit")
	if second := report(goReport, "api", "unit"); second != first {
		t.Fatalf("same report stored under different IDs: %s %s", first, second)
	}
	if corrected := report(goReport, "api", "unit", "--revision", "2", "--region", "internal/unit"); corrected != first {
		t.Fatal("correction metadata changed default report identity")
	}
	if other := report(goReport, "web", "unit"); other == first {
		t.Fatal("member missing from report identity")
	}
	if other := report(goReport, "api", "integration"); other == first {
		t.Fatal("suite missing from report identity")
	}
	if changed := report(strings.ReplaceAll(goReport, "TestOne", "TestTwo"), "api", "unit"); changed == first {
		t.Fatal("report bytes missing from report identity")
	}
	if explicit := report(goReport, "api", "unit", "--run-id", runID); explicit != runID {
		t.Fatal("explicit run ID was ignored")
	}
}

func TestReportCommandAdditionalAdapters(t *testing.T) {
	for format, raw := range map[string]string{
		"pytest": `{"created":1793534402,"duration":2,"exitcode":1,"summary":{"total":3},"tests":[{"nodeid":"tests/test_example.py::test_pass","outcome":"passed","call":{"duration":0.5}},{"nodeid":"tests/test_example.py::test_setup","outcome":"error","setup":{"duration":0.2,"outcome":"failed"}},{"nodeid":"tests/test_example.py::test_skip","outcome":"skipped"}],"environment":{"private":"canary-private-output"}}`,
		"json":   `{"version":1,"started_at":"2026-11-01T12:00:00Z","finished_at":"2026-11-01T12:00:02Z","complete":true,"outcome":"fail","tests":[{"package":"tests/example","test":"pass","outcome":"pass","seconds":0.5},{"package":"tests/example","test":"failure","outcome":"fail","seconds":0.2},{"package":"tests/example","test":"skip","outcome":"skip"}]}`,
	} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Record struct {
						Seconds  float64 `json:"seconds"`
						Complete bool    `json:"complete"`
						Suites   []struct {
							Outcome string `json:"outcome"`
							Tests   struct {
								Counts  struct{ Pass, Fail, Skip int }
								Slowest []struct {
									Seconds float64 `json:"seconds"`
								}
							} `json:"tests"`
						} `json:"suites"`
					} `json:"record"`
				}
				data := new(bytes.Buffer)
				_, _ = data.ReadFrom(r.Body)
				if strings.Contains(data.String(), "canary-private-output") {
					t.Error("private pytest metadata uploaded")
				}
				if json.Unmarshal(data.Bytes(), &body) != nil || body.Record.Seconds != 2 || !body.Record.Complete || len(body.Record.Suites) != 1 {
					t.Errorf("bad normalized body: %s", data.String())
					return
				}
				s := body.Record.Suites[0]
				if s.Outcome != "fail" || s.Tests.Counts.Pass != 1 || s.Tests.Counts.Fail != 1 || s.Tests.Counts.Skip != 1 || len(s.Tests.Slowest) != 2 || s.Tests.Slowest[0].Seconds != 0.5 {
					t.Errorf("lost outcomes or durations: %+v", s)
				}
				_, _ = w.Write([]byte(`{"ok":true,"status":"retained"}`))
			}))
			defer srv.Close()
			configure(t, srv.URL)
			for range 2 {
				var out, stderr bytes.Buffer
				if code := Main([]string{"report", "--format", format, "--file", reportFile(t, raw), "--member", "api", "--suite", "unit"}, &out, &stderr); code != 0 {
					t.Fatalf("adapter refused: %d %s", code, stderr.String())
				}
			}
			if calls != 2 {
				t.Fatalf("uploads=%d", calls)
			}
		})
	}
}

func TestReportCommandProjectBudgetConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".blerg"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaultFile := filepath.Join(root, ".blerg", "test-budgets.json")
	write(defaultFile, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":10}}}`)
	expected := 10.0
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Record struct {
				Suites []struct {
					Layer  string   `json:"layer"`
					Budget *float64 `json:"budget_seconds"`
				} `json:"suites"`
			} `json:"record"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Record.Suites) != 1 || body.Record.Suites[0].Layer != "2" || body.Record.Suites[0].Budget == nil || *body.Record.Suites[0].Budget != expected {
			t.Errorf("budget not preserved: %+v", body)
		}
		_, _ = w.Write([]byte(`{"ok":true,"status":"retained"}`))
	}))
	defer srv.Close()
	configure(t, srv.URL)
	args := []string{"report", "--format", "go", "--file", reportFile(t, goReport), "--member", "api", "--suite", "unit", "--root", root}
	invoke := func(extra ...string) int {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Main(append(append([]string{}, args...), extra...), &out, &stderr)
		if strings.Contains(stderr.String(), root) {
			t.Error("configuration path exposed")
		}
		return code
	}
	if invoke() != 0 || calls != 1 {
		t.Fatal("default project config was not read")
	}
	override := filepath.Join(root, "override.json")
	write(override, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":1}}}`)
	expected = 1
	if invoke("--config", override) != 0 || calls != 2 {
		t.Fatal("explicit project config was not read")
	}
	for _, raw := range []string{`{}`, `{"version":2,"suites":{}}`, `{"version":1,"suites":{"unit":{"layer":"bad","budget_seconds":1}}}`, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":0}}}`, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":-1}}}`, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":1,"private-canary":true}}}`} {
		write(defaultFile, raw)
		before := calls
		if invoke() == 0 || calls != before {
			t.Fatal("invalid config was uploaded")
		}
	}
	if invoke("--config", filepath.Join(root, "missing.json")) == 0 {
		t.Fatal("missing explicit config accepted")
	}
}
