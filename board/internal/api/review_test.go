package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ciTestServer stubs the GitHub endpoints prCIReport calls: the PR
// (for the head sha), check-runs, and combined status. Handlers default to
// "not configured" 404s so a test only needs to set what it exercises.
func ciTestServer(t *testing.T, checkRuns, status http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(ciMux(checkRuns, status))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// ciMux is ciTestServer's routing, separated so a test that needs to swap
// GitHub's answer mid-run can stand up two of them. runs, if given, overrides
// the Actions-runs listing the re-run path reads.
func ciMux(checkRuns, status http.HandlerFunc, runs ...http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"head":{"sha":"deadbeef"}}`)
	})
	if checkRuns != nil {
		mux.HandleFunc("/repos/o/r/commits/deadbeef/check-runs", checkRuns)
	} else {
		mux.HandleFunc("/repos/o/r/commits/deadbeef/check-runs", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"check_runs":[]}`)
		})
	}
	if status != nil {
		mux.HandleFunc("/repos/o/r/commits/deadbeef/status", status)
	} else {
		mux.HandleFunc("/repos/o/r/commits/deadbeef/status", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"state":"pending","total_count":0}`)
		})
	}
	// No Actions runs by default: the sweep asks for these before spending a
	// re-run, and a test that doesn't set them up is testing the note, not the
	// retry. Tests that want the retry pass one in, or use ciRerunServer.
	if len(runs) > 0 && runs[0] != nil {
		mux.HandleFunc("/repos/o/r/actions/runs", runs[0])
	} else {
		mux.HandleFunc("/repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"workflow_runs":[]}`)
		})
	}
	return mux
}

const testPRURL = "https://github.com/o/r/pull/1"

func TestPrCIReportGreenWhenAllChecksSucceed(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[
			{"status":"completed","conclusion":"success"},
			{"status":"completed","conclusion":"neutral"}
		]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciGreen {
		t.Fatalf("state = %v, want ciGreen", rep.state)
	}
}

func TestPrCIReportRedWhenACheckFails(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[
			{"status":"completed","conclusion":"success"},
			{"status":"completed","conclusion":"failure"}
		]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciRed {
		t.Fatalf("state = %v, want ciRed", rep.state)
	}
}

func TestPrCIReportPendingWhileACheckIsRunning(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[
			{"status":"completed","conclusion":"success"},
			{"status":"in_progress","conclusion":""}
		]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciPending {
		t.Fatalf("state = %v, want ciPending", rep.state)
	}
}

// A head commit that reports nothing at all is NOT the same as one whose
// checks are still running: no run is in flight to finish, so the sweep would
// wait forever. ciNone is what lets the sweep say which one it is.
func TestPrCIReportNoneWhenNothingReportsAtAll(t *testing.T) {
	ciTestServer(t, nil, nil) // empty check-runs, zero-count status

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciNone {
		t.Fatalf("state = %v, want ciNone", rep.state)
	}
}

func TestPrCIReportFallsBackToLegacyCombinedStatus(t *testing.T) {
	// no check-runs at all, but a legacy status API context reports success
	ciTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"success","total_count":1}`)
	})

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciGreen {
		t.Fatalf("state = %v, want ciGreen", rep.state)
	}
}

func TestPrCIReportRedOnFailedLegacyStatus(t *testing.T) {
	ciTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"failure","total_count":1}`)
	})

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciRed {
		t.Fatalf("state = %v, want ciRed", rep.state)
	}
}

// TestPrCIReportErrorsOnAPIFailure guards the bug an adversarial review
// caught: a non-2xx GitHub response (rate limit, bad token, outage) must
// surface as an error, not decode into a zero-value "no checks reported"
// result that would make the auto-merge sweep wait forever in silence.
func TestPrCIReportErrorsOnAPIFailure(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"API rate limit exceeded","documentation_url":"https://docs.github.com/rest"}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err == nil {
		t.Fatalf("prCIReport: want error on HTTP 403, got state=%v, nil error", rep.state)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q, want it to mention the HTTP 403", err.Error())
	}
}

func TestPrCIReportErrorsOnUnresolvablePR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	if _, err := prCIReport(context.Background(), testPRURL); err == nil {
		t.Fatal("prCIReport: want error when the PR itself can't be fetched")
	}
}

func TestPrCIReportRejectsUnrecognizedURL(t *testing.T) {
	if _, err := prCIReport(context.Background(), "not-a-pr-url"); err == nil {
		t.Fatal("prCIReport: want error for an unrecognized PR URL")
	}
}

// ── the detail behind the state ─────────────────────────────────────────────

// The author should not have to leave the board to learn what broke, so the
// read has to carry the failing job's name — and the head it belongs to, which
// is what scopes the re-run budget.
func TestPrCIReportNamesEveryFailingCheckAndItsHead(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[
			{"name":"go","status":"completed","conclusion":"failure","completed_at":"2026-08-06T04:46:44Z"},
			{"name":"web","status":"completed","conclusion":"success","completed_at":"2026-08-06T04:40:00Z"},
			{"name":"golangci-lint","status":"completed","conclusion":"timed_out","completed_at":"2026-08-06T04:30:00Z"}
		]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciRed {
		t.Fatalf("state = %v, want ciRed", rep.state)
	}
	if rep.headSHA != "deadbeef" {
		t.Fatalf("headSHA = %q, want deadbeef", rep.headSHA)
	}
	want := []string{"go", "golangci-lint (timed out)"}
	if len(rep.failed) != len(want) {
		t.Fatalf("failed = %q, want %q", rep.failed, want)
	}
	for i := range want {
		if rep.failed[i] != want[i] {
			t.Fatalf("failed = %q, want %q", rep.failed, want)
		}
	}
	// newest is the freshest completion across all checks — it dates the
	// answer, which is how a stale red is told from a new one.
	if got := rep.newest.UTC().Format("15:04:05"); got != "04:46:44" {
		t.Fatalf("newest = %v, want the 04:46:44 completion", rep.newest)
	}
}

// A green head names nothing and dates nothing to worry about, but it must
// still carry the head SHA — the sweep merges on it.
func TestPrCIReportGreenCarriesNoFailures(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[{"name":"go","status":"completed","conclusion":"success"}]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciGreen || len(rep.failed) != 0 || rep.headSHA != "deadbeef" {
		t.Fatalf("report = %+v, want a green deadbeef with no failures", rep)
	}
}

// A finished failure alongside an unfinished check is a head that has not
// finished answering. Calling it red would be wrong twice: GitHub's own merge
// box says pending, and rerun-failed-jobs refuses a run that is still going,
// so the sweep would spend a re-run on a failure that isn't final yet.
func TestPrCIReportPendingOutranksAFinishedFailure(t *testing.T) {
	ciTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"check_runs":[
			{"name":"go","status":"completed","conclusion":"failure","completed_at":"2026-08-06T04:46:44Z"},
			{"name":"web","status":"in_progress","conclusion":""}
		]}`)
	}, nil)

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciPending {
		t.Fatalf("state = %v, want ciPending", rep.state)
	}
	if len(rep.failed) != 0 {
		t.Fatalf("a half-reported head named failures as final: %q", rep.failed)
	}
}

// The legacy status API has no per-check name unless it hands one over, so a
// combined "failure" with no detail still has to produce something a sentence
// can be built around rather than an empty list.
func TestPrCIReportNamesLegacyStatusContexts(t *testing.T) {
	ciTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"failure","total_count":2,"statuses":[
			{"state":"failure","context":"buildkite/test","updated_at":"2026-08-06T04:46:44Z"},
			{"state":"success","context":"buildkite/lint","updated_at":"2026-08-06T04:40:00Z"}
		]}`)
	})

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciRed || len(rep.failed) != 1 || rep.failed[0] != "buildkite/test" {
		t.Fatalf("report = %+v, want red naming buildkite/test", rep)
	}
}

func TestPrCIReportNamesSomethingEvenWithoutStatusDetail(t *testing.T) {
	ciTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"failure","total_count":1}`)
	})

	rep, err := prCIReport(context.Background(), testPRURL)
	if err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if rep.state != ciRed || len(rep.failed) != 1 {
		t.Fatalf("report = %+v, want red with one named failure", rep)
	}
}

// ── asking GitHub to run it again ───────────────────────────────────────────

// rerunFailedCIJobs must re-run the runs that finished badly and leave the
// rest alone: re-running a still-running run is a 403 from GitHub, and
// re-running a green one costs minutes for no information.
func TestRerunFailedCIJobsSkipsRunsThatDidNotFail(t *testing.T) {
	var posted []string
	mux := ciMux(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("head_sha"); got != "deadbeef" {
			t.Errorf("listed runs for head_sha=%q, want deadbeef", got)
		}
		_, _ = fmt.Fprint(w, `{"workflow_runs":[
			{"id":1,"name":"CI","status":"completed","conclusion":"failure"},
			{"id":2,"name":"web","status":"completed","conclusion":"success"},
			{"id":3,"name":"slow","status":"in_progress","conclusion":""},
			{"id":4,"name":"lint","status":"completed","conclusion":"timed_out"}
		]}`)
	})
	for _, id := range []string{"1", "2", "3", "4"} {
		mux.HandleFunc("/repos/o/r/actions/runs/"+id+"/rerun-failed-jobs",
			func(w http.ResponseWriter, r *http.Request) {
				posted = append(posted, r.URL.Path)
				w.WriteHeader(http.StatusCreated)
			})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	names, err := rerunFailedCIJobs(context.Background(), testPRURL, "deadbeef")
	if err != nil {
		t.Fatalf("rerunFailedCIJobs: %v", err)
	}
	if len(posted) != 2 {
		t.Fatalf("re-ran %v, want only the failed and timed-out runs", posted)
	}
	if len(names) != 2 || names[0] != "CI" || names[1] != "lint" {
		t.Fatalf("names = %q, want [CI lint]", names)
	}
}

// A token without actions:write is the realistic failure here, and it must
// surface as an error so the caller falls through to telling the human rather
// than claiming a retry that never happened.
func TestRerunFailedCIJobsReportsARefusal(t *testing.T) {
	mux := ciMux(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"workflow_runs":[{"id":1,"name":"CI","status":"completed","conclusion":"failure"}]}`)
	})
	mux.HandleFunc("/repos/o/r/actions/runs/1/rerun-failed-jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	names, err := rerunFailedCIJobs(context.Background(), testPRURL, "deadbeef")
	if err == nil {
		t.Fatalf("want an error on a refused re-run, got names=%q", names)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q, want it to name the HTTP 403", err)
	}
}

// A red head with no Actions run behind it (an external CI's commit status) is
// not an error — there is simply nothing here that can be re-run.
func TestRerunFailedCIJobsIsANoOpWithoutActionsRuns(t *testing.T) {
	ciTestServer(t, nil, nil) // ciMux serves an empty workflow_runs list

	names, err := rerunFailedCIJobs(context.Background(), testPRURL, "deadbeef")
	if err != nil || len(names) != 0 {
		t.Fatalf("names=%q err=%v, want no runs and no error", names, err)
	}
}

// The whole self-healing path depends on a re-run RETIRING the failure that
// prompted it. Re-running adds an attempt to the same head, so only the
// latest-per-name view of the checks stops reporting the old red — under
// filter=all the sweep would read that failure forever and never merge.
// It is GitHub's default, which is exactly why it is easy to drop by
// accident, so the request has to keep asking for it out loud.
func TestPrCIReportAsksForTheLatestCheckRunPerName(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"head":{"sha":"deadbeef"}}`)
	})
	mux.HandleFunc("/repos/o/r/commits/deadbeef/check-runs", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"check_runs":[{"name":"go","status":"completed","conclusion":"success"}]}`)
	})
	mux.HandleFunc("/repos/o/r/commits/deadbeef/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"state":"pending","total_count":0}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	if _, err := prCIReport(context.Background(), testPRURL); err != nil {
		t.Fatalf("prCIReport: %v", err)
	}
	if !strings.Contains(query, "filter=latest") {
		t.Fatalf("check-runs query = %q, want it to ask for filter=latest", query)
	}
}

func TestShortSHA(t *testing.T) {
	for in, want := range map[string]string{
		"45277e2abcdef": "45277e2",
		"45277e2":       "45277e2",
		"abc":           "abc",
		"":              "",
	} {
		if got := shortSHA(in); got != want {
			t.Errorf("shortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}
