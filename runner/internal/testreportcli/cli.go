// Package testreportcli reports project test measurements through the session API.
package testreportcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/testreport"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

const Usage = "usage: blerg-runner test report --format go|vitest|pytest|json --file FILE --member MEMBER --suite NAME [--run-id UUID] [--revision N] [--commit SHA] [--region RELATIVE_PATH] [--root PROJECT_ROOT] [--config FILE]"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func Main(args []string, out, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = fmt.Fprintln(out, Usage)
		return 0
	}
	if err := run(args, out); err != nil {
		failurelog.Default.Event("test_report_cli", slog.LevelWarn, "component", "test-report-cli", "stage", "report", "cause", failurelog.Cause(err))
		_, _ = fmt.Fprintln(stderr, "Test report:", err)
		return 1
	}
	return 0
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "report" {
		return errors.New(Usage)
	}
	fs := flag.NewFlagSet("test report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "", "go, vitest, pytest, or json")
	file := fs.String("file", "", "existing machine-readable report")
	member := fs.String("member", "", "selected workspace member ID")
	suite := fs.String("suite", "", "suite name")
	id := fs.String("run-id", "", "reuse to retry the same report")
	revision := fs.Int64("revision", 1, "positive correction revision")
	commit := fs.String("commit", "", "commit SHA, omitted when unknown")
	region := fs.String("region", "", "relative selected test region, omitted when unknown")
	config := fs.String("config", "", "project test budget configuration; defaults to .blerg/test-budgets.json under root")
	root := fs.String("root", "", "project root for relative filenames and budgets; defaults to working directory")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(out, Usage)
			return err
		}
		return errors.New(Usage)
	}
	if fs.NArg() != 0 || (*format != "go" && *format != "vitest" && *format != "pytest" && *format != "json") || *file == "" || !safeText(*member, 128) || *revision < 1 || (*id != "" && !uuidPattern.MatchString(*id)) || (*commit != "" && !protocol.ValidSessionOriginCommit(*commit)) || (*region != "" && !testreport.ValidRegion(*region)) {
		return errors.New(Usage)
	}
	base, err := url.Parse(serverHTTP())
	sid := os.Getenv("BLERG_RUNNER_SESSION_ID")
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || !uuidPattern.MatchString(sid) {
		return errors.New("a valid session HTTP endpoint and session ID are required")
	}
	token, err := sessionToken(sid)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("a session messaging token is required")
	}
	if *root == "" {
		*root, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("project root is unavailable (%s)", failurelog.Cause(err))
		}
	}
	*root, err = filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("project root is unavailable (%s)", failurelog.Cause(err))
	}
	input, err := os.Open(*file) // #nosec G304 -- operator-selected, read-only machine-readable report; never executed.
	if err != nil {
		return fmt.Errorf("report file cannot be opened (%s)", failurelog.Cause(err))
	}
	defer func() { _ = input.Close() }()
	digest := sha256.New()
	identity, _ := json.Marshal([]string{"blerg:test-report:v1", *member, *suite})
	_, _ = digest.Write(identity) // hash.Hash.Write always succeeds.
	reportBytes := io.TeeReader(input, digest)
	var parsed testreport.Report
	switch *format {
	case "go":
		parsed, err = testreport.GoJSON(reportBytes, *suite)
	case "vitest":
		parsed, err = testreport.VitestJSON(reportBytes, *suite, *root)
	case "pytest":
		parsed, err = testreport.PytestJSON(reportBytes, *suite)
	case "json":
		parsed, err = testreport.GenericJSON(reportBytes, *suite)
	}
	if err != nil {
		return err
	}
	if err = applyBudget(&parsed, *root, *config, *suite); err != nil {
		return err
	}
	if *id == "" {
		bytes := digest.Sum(nil)[:16]
		bytes[6] = (bytes[6] & 0x0f) | 0x80
		bytes[8] = (bytes[8] & 0x3f) | 0x80
		*id = fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
	}
	if _, err = fmt.Fprintf(out, "Run %s; revision %d. Reuse this run ID when retrying.\n", *id, *revision); err != nil {
		return err
	}
	var knownCommit *string
	if *commit != "" {
		knownCommit = commit
	}
	record := map[string]any{"id": *id, "commit": knownCommit, "started_at": parsed.StartedAt.UTC().Format(time.RFC3339Nano), "finished_at": parsed.FinishedAt.UTC().Format(time.RFC3339Nano), "seconds": parsed.Suite.Seconds, "complete": parsed.Complete, "suites": []testtiming.Suite{parsed.Suite}}
	var selectedRegion *string
	if *region != "" {
		selectedRegion = region
	}
	body, err := json.Marshal(map[string]any{"member_id": *member, "revision": *revision, "source": "custom", "region": selectedRegion, "cached_packages": parsed.CachedPackages, "record": record})
	if err != nil || len(body) > 256<<10 {
		return errors.New("normalized report exceeds import limits")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(base.String(), "/") + "/api/sessions/" + url.PathEscape(sid) + "/test-runs/import"
	// #nosec G704 -- only the session-configured Runner endpoint is used; report data never selects a URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid session endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	// #nosec G704 -- authenticated session endpoint; redirects are refused.
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("report transport failed (%s)", failurelog.Cause(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return fmt.Errorf("report response read failed (%s)", failurelog.Cause(err))
	}
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("%s (HTTP %d)", publicMessage(raw), resp.StatusCode)
	}
	var receipt struct {
		OK         bool   `json:"ok"`
		Superseded bool   `json:"superseded"`
		Status     string `json:"status"`
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &receipt) != nil || !receipt.OK || (receipt.Status != "retained" && receipt.Status != "not_retained") {
		return errors.New("invalid report receipt")
	}
	status := receipt.Status
	if receipt.Superseded {
		status = "superseded"
	}
	if parsed.CachedPackages > 0 {
		if _, err = fmt.Fprintf(out, "Omitted %d cached packages; run marked incomplete.\n", parsed.CachedPackages); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "Report %s.\n", status)
	return err
}

func safeText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func sessionToken(sid string) (string, error) {
	if !uuidPattern.MatchString(sid) {
		return "", errors.New("invalid session ID")
	}
	token := os.Getenv("BLERG_RUNNER_SESSION_TOKEN")
	state := os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("session state is unavailable (%s)", failurelog.Cause(err))
		}
		state = filepath.Join(home, ".blerg-runner-daemon")
	}
	// #nosec G304 G703 -- UUID-validated own session token inside the private state directory.
	raw, err := os.ReadFile(filepath.Join(state, "session-tokens", sid))
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("session token read failed (%s)", failurelog.Cause(err))
	}
	return token, nil
}

func serverHTTP() string {
	clean := func(key string) string { return strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/") }
	if base := clean("BLERG_RUNNER_SERVER_HTTP"); base != "" {
		return base
	}
	if preview := clean("BLERG_RUNNER_PREVIEW_URL"); strings.HasSuffix(preview, "/api/preview") {
		return strings.TrimSuffix(preview, "/api/preview")
	}
	ws := strings.TrimSuffix(clean("BLERG_RUNNER_SERVER_URL"), "/ws/daemon")
	if strings.HasPrefix(ws, "ws://") {
		return "http://" + strings.TrimPrefix(ws, "ws://")
	}
	if strings.HasPrefix(ws, "wss://") {
		return "https://" + strings.TrimPrefix(ws, "wss://")
	}
	return ""
}

func publicMessage(raw []byte) string {
	var reply struct {
		Error string `json:"error"`
	}
	if len(raw) <= 4096 && json.Unmarshal(raw, &reply) == nil {
		switch reply.Error {
		case "Invalid test history.", "Invalid test report metadata.", "Test history is unavailable.", "Select a project in this workspace.", "Invalid test run or run outside the 180-day retention window.", "Test run revision conflicts with stored history.", "Workspace not found.":
			return reply.Error
		}
	}
	return "Report refused; server error details are unavailable."
}

// LoadBudgets reads the operator-selected project's configuration. A missing
// default is unknown; missing explicit configuration is an error.
func LoadBudgets(root, override string) (map[string]testreport.SuiteBudget, error) {
	path := override
	if path == "" {
		path = filepath.Join(root, ".blerg", "test-budgets.json")
	}
	file, err := os.Open(path) // #nosec G304 -- explicit read-only project configuration, never executed.
	if err != nil {
		if override == "" && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("budget configuration cannot be opened (%s)", failurelog.Cause(err))
	}
	defer func() { _ = file.Close() }()
	return testreport.ReadBudgets(file)
}
func applyBudget(report *testreport.Report, root, override, suite string) error {
	budgets, err := LoadBudgets(root, override)
	if err != nil {
		return err
	}
	if budget, ok := budgets[suite]; ok {
		report.Suite.Layer = budget.Layer
		report.Suite.BudgetSeconds = budget.Seconds
	}
	return nil
}
