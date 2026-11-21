// test-performance imports existing gate history. It never executes tests.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/testreportcli"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "report" {
		os.Exit(testreportcli.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "test-performance:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("test-performance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	history := flags.String("history", "", "gate history JSONL to import (read only)")
	member := flags.String("member", "", "authorized workspace member ID; repository for legacy scalar sessions")
	root := flags.String("root", ".", "project root for test budgets")
	config := flags.String("config", "", "explicit project budget configuration")
	revision := flags.Int64("revision", 1, "positive correction revision")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid flags: use -history FILE -member MEMBER [-revision N]")
	}
	if *history == "" || *member == "" || *revision < 1 || flags.NArg() != 0 {
		return errors.New("require -history FILE -member MEMBER and a positive revision")
	}
	budgets, err := testreportcli.LoadBudgets(*root, *config)
	if err != nil {
		return err
	}
	base, err := url.Parse(os.Getenv("BLERG_RUNNER_SERVER_HTTP"))
	sid := os.Getenv("BLERG_RUNNER_SESSION_ID")
	token := os.Getenv("BLERG_RUNNER_SESSION_TOKEN")
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || sid == "" || token == "" {
		return errors.New("session HTTP endpoint, session ID and messaging token are required")
	}
	file, err := os.Open(*history)
	if err != nil {
		return fmt.Errorf("history cannot be opened (%s)", failurelog.Cause(err))
	}
	defer func() { _ = file.Close() }()
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 64<<10), 256<<10)
	imported, expired, line, superseded, discarded := 0, 0, 0, 0, 0
	for scan.Scan() {
		line++
		if strings.TrimSpace(scan.Text()) == "" {
			continue
		}
		var record testtiming.Record
		if json.Unmarshal(scan.Bytes(), &record) != nil {
			return fmt.Errorf("history line %d: invalid JSON", line)
		}
		at, err := time.Parse(time.RFC3339Nano, record.FinishedAt)
		if err != nil {
			return fmt.Errorf("history line %d: missing or invalid finish time", line)
		}
		if at.Before(time.Now().AddDate(0, 0, -180)) {
			expired++
			continue
		}
		// Keep absent counts absent while discarding host/log metadata.
		var shape struct {
			Seconds  *float64 `json:"seconds"`
			Complete *bool    `json:"complete"`
			Suites   []struct {
				Seconds *float64 `json:"seconds"`
				Tests   struct {
					Counts json.RawMessage `json:"counts"`
				} `json:"tests"`
			} `json:"suites"`
		}
		if json.Unmarshal(scan.Bytes(), &shape) != nil || shape.Seconds == nil || shape.Complete == nil {
			return fmt.Errorf("history line %d: invalid suite data", line)
		}
		suites := make([]map[string]any, 0, len(record.Suites))
		for i, suite := range record.Suites {
			if shape.Suites[i].Seconds == nil {
				return fmt.Errorf("history line %d: missing suite duration", line)
			}
			slowest := suite.Tests.Slowest
			if slowest == nil {
				slowest = []testtiming.TestTiming{}
			}
			detail := map[string]any{"slowest": slowest}
			if suite.Tests.FlakeReruns != nil {
				detail["flake_reruns"] = suite.Tests.FlakeReruns
			}
			if len(shape.Suites[i].Tests.Counts) > 0 {
				detail["counts"] = shape.Suites[i].Tests.Counts
			}
			row := map[string]any{"suite": suite.Suite, "seconds": suite.Seconds, "outcome": suite.Outcome, "tests": detail}
			if suite.Layer != "" {
				row["layer"] = suite.Layer
			}
			if suite.BudgetSeconds != nil {
				row["budget_seconds"] = suite.BudgetSeconds
			}
			if budget, ok := budgets[suite.Suite]; ok && suite.Layer == "" && suite.BudgetSeconds == nil {
				row["layer"] = budget.Layer
				row["budget_seconds"] = budget.Seconds
			}
			suites = append(suites, row)
		}
		var commit *string
		if record.Commit != "" {
			commit = &record.Commit
		}
		body, err := json.Marshal(map[string]any{"member_id": *member, "revision": *revision, "record": map[string]any{"id": record.ID, "commit": commit, "started_at": record.StartedAt, "finished_at": record.FinishedAt, "seconds": record.Seconds, "complete": record.Complete, "suites": suites}})
		if err != nil {
			return fmt.Errorf("history line %d: invalid numeric measurement", line)
		}
		endpoint := strings.TrimRight(base.String(), "/") + "/api/sessions/" + url.PathEscape(sid) + "/test-runs/import"
		// #nosec G704 -- only the session-configured Runner endpoint is used; no report data selects a URL.
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return errors.New("invalid session endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		// #nosec G704 -- the session-configured Runner endpoint is intentional; redirects are refused.
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("history line %d: report transport failed (%s)", line, failurelog.Cause(err))
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("history line %d: response read failed (%s)", line, failurelog.Cause(readErr))
		}
		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return fmt.Errorf("history line %d: %s (HTTP %d); %d prior rows imported", line, publicReportMessage(raw), resp.StatusCode, imported)
		}
		var receipt struct {
			Superseded bool   `json:"superseded"`
			Status     string `json:"status"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &receipt) != nil {
			return fmt.Errorf("history line %d: invalid report receipt", line)
		}
		switch {
		case receipt.Superseded:
			superseded++
		case receipt.Status == "not_retained":
			discarded++
		default:
			imported++
		}
	}
	if scan.Err() != nil {
		return fmt.Errorf("history read failed or line exceeds 256 KiB (%s)", failurelog.Cause(scan.Err()))
	}
	_, err = fmt.Fprintf(out, "Imported %d runs; superseded %d; not retained %d; skipped %d runs older than 180 days.\n", imported, superseded, discarded, expired)
	return err
}

// Only public messages defined by this API are printable. Arbitrary upstream
// bodies may contain paths, logs or credentials and never become diagnostics.
func publicReportMessage(raw []byte) string {
	var reply struct {
		Error string `json:"error"`
	}
	if len(raw) <= 4096 && json.Unmarshal(raw, &reply) == nil {
		switch reply.Error {
		case "Invalid test history.", "Test history is unavailable.", "Select a project in this workspace.", "Invalid test run or run outside the 180-day retention window.", "Test run revision conflicts with stored history.", "Workspace not found.":
			return reply.Error
		}
	}
	return "Report refused; server error details are unavailable."
}
