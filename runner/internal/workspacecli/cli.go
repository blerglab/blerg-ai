// Package workspacecli implements workspace telemetry commands in Go. The
// existing messaging CLI dispatches here using the installed runtime binary.
package workspacecli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
)

func Main(args []string, out, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: blerg-runner workspace skills | skill-evidence --skill NAME --version HASH --rule RULE --result followed|missed|unknown | metrics [--json] [--session ID] [--turn SEQ] [--card BOARD#N] | run [flags]")
		return 2
	}
	if err := run(args, out, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func run(args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("workspace "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print JSON")
	session := fs.String("session", "", "select a session in this workspace")
	turn := fs.String("turn", "", "select a turn by its ending event sequence (0 for open)")
	card := fs.String("card", "", "select a card as BOARD#N")
	events := fs.String("events", "", "audit a local event JSON file instead of contacting the server")
	imports := fs.String("import-gates", "", "import BLG-368 gate JSONL, or - for stdin")
	started := fs.String("started-at", "", "registered run start (RFC3339)")
	ended := fs.String("ended-at", "", "registered run end (RFC3339)")
	id := fs.String("id", "", "stable run ID")
	suite := fs.String("suite", "", "suite name")
	commit := fs.String("commit", "unknown", "commit being checked")
	state := fs.String("state", "running", "running, finished, failed, or stopped")
	progress := fs.Int64("progress", 0, "completed work units; unchanged values do not count as progress")
	skill := fs.String("skill", "", "skill name")
	version := fs.String("version", "", "exact loaded skill version")
	rule := fs.String("rule", "", "skill rule")
	result := fs.String("result", "unknown", "followed, missed or unknown")
	full := fs.Bool("full-suite", false, "this is a full suite run")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if args[0] == "metrics" && *events != "" {
		return audit(*events, out)
	}
	if args[0] == "metrics" && *imports != "" {
		return importGates(*imports, out, stderr)
	}
	root := os.Getenv("BLERG_RUNNER_SESSION_ID")
	base := strings.TrimRight(os.Getenv("BLERG_RUNNER_SERVER_HTTP"), "/")
	token := os.Getenv("BLERG_RUNNER_SESSION_TOKEN")
	stateDir := os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR")
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".blerg-runner-daemon")
	}
	if root != "" && filepath.Base(root) == root {
		//nolint:gosec // CLI reads its own refreshed token from the configured private state directory.
		if raw, err := os.ReadFile(filepath.Join(stateDir, "session-tokens", root)); err == nil {
			token = strings.TrimSpace(string(raw))
		}
	}
	if root == "" || base == "" || token == "" {
		return fmt.Errorf("workspace metrics need a configured session")
	}
	path := "/api/sessions/" + url.PathEscape(root) + "/workspace/metrics"
	query := url.Values{}
	var body io.Reader
	method := http.MethodGet
	switch args[0] {
	case "skill-evidence":
		raw, _ := json.Marshal(map[string]string{"skill": *skill, "version": *version, "rule": *rule, "result": *result})
		body = strings.NewReader(string(raw))
		method = http.MethodPost
		path = "/api/sessions/" + url.PathEscape(root) + "/skill-evidence"
	case "skills":
		query.Set("session", *session)
		if *card != "" {
			query.Set("card", *card)
		}
	case "metrics":
		if *session != "" {
			query.Set("session", *session)
		}
		if *turn != "" {
			query.Set("turn", *turn)
		}
		if *card != "" {
			query.Set("card", *card)
		}
		if !*jsonOut && *turn == "" {
			query.Set("format", "table")
		}
	case "run":
		if *id == "" || *suite == "" {
			return fmt.Errorf("run needs --id and --suite")
		}
		registration := activity.Run{ID: *id, Suite: *suite, Commit: *commit, State: *state, Progress: *progress, FullSuite: *full, Card: *card}
		if *started != "" {
			at, err := time.Parse(time.RFC3339Nano, *started)
			if err != nil {
				return fmt.Errorf("invalid start time")
			}
			registration.StartedAt = at
		}
		if *ended != "" {
			at, err := time.Parse(time.RFC3339Nano, *ended)
			if err != nil {
				return fmt.Errorf("invalid end time")
			}
			registration.EndedAt = &at
		}
		raw, _ := json.Marshal(registration)
		body = strings.NewReader(string(raw))
		method = http.MethodPost
		path += "/runs"
	default:
		return fmt.Errorf("unknown workspace command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	//nolint:gosec // CLI connects to its configured runner; redirects are refused below.
	req, err := http.NewRequestWithContext(ctx, method, base+path+"?"+query.Encode(), body)
	if err != nil {
		return fmt.Errorf("invalid runner URL")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	//nolint:gosec // Authenticated CLI request to the configured runner, with redirects disabled.
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runner telemetry request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("runner telemetry returned HTTP %d", resp.StatusCode)
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return fmt.Errorf("could not read runner telemetry")
	}
	if len(response) > 16<<20 {
		return fmt.Errorf("telemetry is too large; select a session or turn")
	}
	if args[0] == "skills" {
		var workspace activity.Workspace
		if json.Unmarshal(response, &workspace) != nil {
			return fmt.Errorf("invalid skills reply")
		}
		reports := map[string]any{}
		for _, worker := range workspace.Workers {
			reports[worker.SessionID] = worker.Skills
		}
		response, _ = json.MarshalIndent(reports, "", "  ")
		response = append(response, '\n')
	}
	_, err = out.Write(response)
	return err
}

func audit(path string, out io.Writer) error {
	f, err := os.Open(path) //nolint:gosec // The CLI user explicitly selected this local telemetry file.
	if err != nil {
		return fmt.Errorf("cannot read event log")
	}
	defer func() { _ = f.Close() }()
	var rows []struct {
		Seq     int64           `json:"seq"`
		At      time.Time       `json:"ts"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err = json.NewDecoder(io.LimitReader(f, 256<<20)).Decode(&rows); err != nil {
		return fmt.Errorf("invalid event log")
	}
	if len(rows) == 0 {
		return fmt.Errorf("empty event log")
	}
	var events []activity.Event
	start, end := rows[0].At, rows[0].At
	for _, r := range rows {
		events = append(events, activity.Event{Seq: r.Seq, At: r.At, Kind: r.Kind, Payload: r.Payload})
		if r.At.Before(start) {
			start = r.At
		}
		if r.At.After(end) {
			end = r.At
		}
	}
	turns := activity.Turns(start, end, events, nil)
	report := struct {
		EventLog         string          `json:"event_log"`
		Turns            []activity.Turn `json:"turns"`
		Seconds          float64         `json:"seconds"`
		TurnSeconds      float64         `json:"turn_seconds"`
		TurnGateFraction float64         `json:"turn_gate_fraction"`
		GateFraction     float64         `json:"gate_fraction"`
		ModelFraction    float64         `json:"model_fraction"`
	}{Turns: turns, EventLog: filepath.Base(path)}
	var gates, model float64
	for _, t := range turns {
		report.TurnSeconds += t.DurationSeconds
		gates += t.Timing.Phases["gates"]
		model += t.Timing.Seconds["model"]
	}
	report.Seconds = end.Sub(start).Seconds()
	if report.TurnSeconds > 0 {
		report.TurnGateFraction = gates / report.TurnSeconds
	}
	if report.Seconds > 0 {
		report.GateFraction = gates / report.Seconds
		report.ModelFraction = model / report.Seconds
	}
	return json.NewEncoder(out).Encode(report)
}

// importGates consumes the existing gate recorder format. It keeps suite-level
// intervals and commit identities, and never imports test output or commands.
func importGates(path string, out, stderr io.Writer) error {
	var input io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path) //nolint:gosec // The CLI user explicitly selected this local telemetry file.
		if err != nil {
			return fmt.Errorf("cannot open gate telemetry")
		}
		defer func() { _ = f.Close() }()
		input = f
	}
	dec := json.NewDecoder(io.LimitReader(input, 16<<20))
	imported := 0
	for {
		var record struct {
			ID     string `json:"id"`
			Commit string `json:"commit"`
			Full   bool   `json:"full_matrix"`
			Suites []struct {
				Name      string    `json:"suite"`
				Seconds   float64   `json:"seconds"`
				ExitCode  int       `json:"exit_code"`
				StartedAt time.Time `json:"started_at"`
			} `json:"suites"`
		}
		if err := dec.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("invalid gate telemetry")
		}
		for _, suite := range record.Suites {
			if suite.StartedAt.IsZero() || suite.Seconds < 0 || suite.Seconds > 7*24*3600 {
				return fmt.Errorf("invalid gate interval")
			}
			state := "finished"
			if suite.ExitCode != 0 {
				state = "failed"
			}
			commit := record.Commit
			if commit == "" {
				commit = "unknown"
			}
			args := []string{"run", "--id", record.ID + "-" + suite.Name, "--suite", suite.Name, "--commit", commit, "--state", state, "--progress", "1", "--started-at", suite.StartedAt.Format(time.RFC3339Nano), "--ended-at", suite.StartedAt.Add(time.Duration(suite.Seconds * float64(time.Second))).Format(time.RFC3339Nano)}
			if record.Full {
				args = append(args, "--full-suite")
			}
			if err := run(args, io.Discard, stderr); err != nil {
				return err
			}
			imported++
		}
	}
	_, err := fmt.Fprintf(out, "Imported %d suite intervals.\n", imported)
	return err
}
