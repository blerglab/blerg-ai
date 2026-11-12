// Package plancli implements bounded, explicitly untrusted blackboard access for agents.
package plancli

import (
	"bytes"
	"context"
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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

const MaxOutputBytes = 32 << 10
const maxResponseBytes = 1 << 20
const Usage = "usage: blerg-runner plan show|history|propose|withdraw|draft [--blackboard ID] [--cursor N] [--limit N] [--changes JSON] [--plan JSON] [milestone|board#card]\n" +
	"withdraw PROPOSAL_ID removes only workspace Main's own pending proposal; it never changes committed scope.\n" +
	"--changes is a JSON array. Item changes use {\"operation\":\"add\"|\"remove\"|\"move\",\"item\":{...}}. " +
	"A board card: {\"board_id\":\"ID\",\"card_number\":1,\"thrust_id\":\"ID\",\"reason\":\"Bug\"} to add; " +
	"remove/move need \"reason\" and, for move, \"destination\" instead of \"thrust_id\". " +
	"A plain item (no board): {\"title\":\"Write the draft\",\"goal_id\":\"ID\"}; remove needs only \"id\" from a prior read.\n" +
	"Link/unlink a board in a separate proposal: {\"operation\":\"link\"|\"unlink\",\"item\":{\"board_id\":\"ID\",\"reason\":\"...\"}}. Then Main raises ask --plan-proposal ID --revision N. Unlink refuses boards referenced by items or milestones. " +
	"Open a milestone (owner approval required): {\"operation\":\"milestone_add\",\"milestone\":{\"name\":\"0.2.0\",\"board_id\":\"ID\",\"release_id\":\"ID\",\"target\":\"2027-01-02\",\"description\":\"Next work\"}}. Only name is required; target uses YYYY-MM-DD. A sole linked board is inferred; name must exactly match a board release when releases exist, otherwise use a short name (60 characters). Propose at workspace scope (omit the milestone selector), then Main raises ask --person --blackboard ID --plan-proposal ID --revision N. Only the owner-bound reply commits.\n" +
	"Thrust changes (owner approval required): " +
	"{\"operation\":\"thrust_add\",\"thrust\":{\"name\":\"...\",\"goal_id\":\"ID\",\"reason\":\"...\"}}\n" +
	"{\"operation\":\"thrust_rename\",\"thrust\":{\"id\":\"ID\",\"name\":\"...\",\"reason\":\"...\"}}\n" +
	"{\"operation\":\"thrust_merge\",\"thrust\":{\"id\":\"ID\",\"destination\":\"ID\",\"reason\":\"...\"}}\n" +
	"{\"operation\":\"thrust_remove\",\"thrust\":{\"id\":\"ID\",\"reason\":\"...\"}}\n" +
	"Merge moves all cards, then removes the source; remove refuses any remaining cards. " +
	"draft asks the server to propose a vision, goals, a first milestone and thrusts with reasons for this " +
	"workspace, and stores it as a pending draft; it never commits. As Main, raise a person-required bound ask with " +
	"--draft ID --revision N; the owner answers in chat or Needs you. With no --plan, the draft holds only real facts (the " +
	"workspace name and its linked board). --plan is a JSON object of what you actually drafted; a field " +
	"you omit stays empty, never invented: " +
	"{\"vision\":\"...\",\"goals\":[\"...\"],\"milestone\":{\"name\":\"...\",\"release_id\":\"...\",\"target\":\"2026-03-01\"}," +
	"\"thrusts\":[{\"name\":\"...\",\"goal_index\":0,\"reason\":\"...\"}]}."

// usageError carries the whole usage text, which ends in punctuation, as the error.
type usageError struct{}

func (usageError) Error() string { return Usage }

func Main(args []string, out, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = fmt.Fprintln(out, Usage)
		return 0
	}
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, Usage)
		return 2
	}
	if err := run(args, out, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		failurelog.Default.Event("plan_cli", slog.LevelError, "component", "plan-cli", "stage", "request", "cause", failureCause(err))
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func run(args []string, out, stderr io.Writer) error {
	command := args[0]
	if command != "show" && command != "history" && command != "propose" && command != "draft" && command != "withdraw" {
		return usageError{}
	}
	fs := flag.NewFlagSet("plan "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, Usage); fs.PrintDefaults() }
	bid := fs.String("blackboard", "", "blackboard ID (required when more than one exists)")
	cursor := fs.String("cursor", "", "next cursor from a previous page")
	limit := fs.Int("limit", 50, "rows per page, 1–100")
	changes := fs.String("changes", "", "JSON array of proposed changes; never commits")
	plan := fs.String("plan", "", "JSON object of draft content (draft only); omitted fields stay empty")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{}
	}
	if fs.NArg() > 1 || *limit < 1 || *limit > 100 || len(*bid) > 100 || len(*cursor) > 100 {
		return usageError{}
	}
	if command == "draft" && (fs.NArg() != 0 || *changes != "" || *bid != "" || *cursor != "") {
		return planError("draft takes no arguments or flags other than --plan")
	}
	if command != "draft" && *plan != "" {
		return planError("--plan is only for draft")
	}
	root := os.Getenv("BLERG_RUNNER_SESSION_ID")
	base := serverHTTP()
	token := os.Getenv("BLERG_RUNNER_SESSION_TOKEN")
	stateDir := os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR")
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".blerg-runner-daemon")
	}
	if root != "" && filepath.Base(root) == root {
		//nolint:gosec // Only this session's own refreshed token in the private state directory.
		if raw, err := os.ReadFile(filepath.Join(stateDir, "session-tokens", root)); err == nil {
			token = strings.TrimSpace(string(raw))
		}
	}
	if base == "" {
		return planError("no server address")
	}
	if root == "" || token == "" {
		return planError("plan access needs a configured session")
	}
	query := url.Values{"operation": {command}, "limit": {fmt.Sprint(*limit)}}
	if *bid != "" {
		query.Set("blackboard", *bid)
	}
	if *cursor != "" {
		query.Set("cursor", *cursor)
	}
	if fs.NArg() == 1 && command != "withdraw" {
		selector := fs.Arg(0)
		if command == "history" && strings.Contains(selector, "#") {
			query.Set("card", selector)
		} else {
			query.Set("milestone", selector)
		}
	}
	method := http.MethodGet
	var body io.Reader
	switch command {
	case "withdraw":
		if fs.NArg() != 1 || len(fs.Arg(0)) > 100 || *changes != "" || *cursor != "" {
			return planError("withdraw needs one proposal ID (Main only)")
		}
		data, _ := json.Marshal(map[string]string{"proposal_id": fs.Arg(0)})
		body = bytes.NewReader(data)
		method = http.MethodPost

	case "propose":
		var batch []json.RawMessage
		if len(*changes) > 64<<10 || json.Unmarshal([]byte(*changes), &batch) != nil || len(batch) == 0 || len(batch) > 100 {
			return planError("propose needs --changes with a JSON array of 1–100 changes")
		}
		data, _ := json.Marshal(map[string]any{"changes": batch})
		body = bytes.NewReader(data)
		method = http.MethodPost
	case "draft":
		method = http.MethodPost
		if *plan != "" {
			if len(*plan) > 64<<10 || !json.Valid([]byte(*plan)) {
				return planError("--plan must be a JSON object, at most 64KB")
			}
			body = strings.NewReader(*plan)
		}
	default:
		if *changes != "" {
			return planError("--changes is only for propose")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	endpoint := base + "/api/sessions/" + url.PathEscape(root) + "/plan?" + query.Encode()
	if command == "draft" {
		endpoint = base + "/api/sessions/" + url.PathEscape(root) + "/plan/draft"
	}
	//nolint:gosec // CLI connects only to its configured runner; redirects are refused.
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return planError("plan server is not configured correctly")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	//nolint:gosec // Authenticated request to the configured runner, with redirects disabled.
	resp, err := client.Do(req)
	if err != nil {
		return planError("plan server could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
		if readErr == nil && len(data) <= 4096 && json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return &refusalError{status: resp.StatusCode, reason: problem.Error}
		}
		return planError(fmt.Sprintf("plan request refused (HTTP %d)", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || !json.Valid(data) {
		return planError("plan server returned an invalid or oversized response")
	}
	return PrintUntrusted(out, data)
}

// PrintUntrusted encodes the entire response as a JSON string in a labeled field.
// External closing tags, quotes and line breaks cannot escape it. The byte cap
// includes escaping and metadata, and truncation is always explicit.
func PrintUntrusted(out io.Writer, data []byte) error {
	var page struct {
		NextCursor string `json:"next_cursor"`
		Truncated  bool   `json:"truncated"`
	}
	_ = json.Unmarshal(data, &page)
	if len(page.NextCursor) > 100 {
		page.NextCursor = ""
		page.Truncated = true
	}
	encode := func(n int) []byte {
		for n > 0 && !utf8.Valid(data[:n]) {
			n--
		}
		payload := struct {
			Untrusted  string `json:"untrusted_blackboard_data"`
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"next_cursor,omitempty"`
			Retry      bool   `json:"retry_with_smaller_limit,omitempty"`
		}{string(data[:n]), page.Truncated || n < len(data), page.NextCursor, n < len(data)}
		if n < len(data) {
			payload.NextCursor = ""
		}
		encoded, _ := json.Marshal(payload)
		return encoded
	}
	lo, hi := 0, len(data)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(encode(mid))+1 <= MaxOutputBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	encoded := encode(lo)
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return planError("plan output could not be written")
	}
	return nil
}

// planError contains only local, fixed diagnostics, never upstream or environment data.
type planError string

func (e planError) Error() string { return string(e) }

type refusalError struct {
	status int
	reason string
}

func (e *refusalError) Error() string {
	return fmt.Sprintf("plan request refused (HTTP %d): %q", e.status, e.reason)
}
func failureCause(err error) string {
	var refusal *refusalError
	if errors.As(err, &refusal) {
		return fmt.Sprintf("http_%d", refusal.status)
	}
	var diagnostic planError
	if errors.As(err, &diagnostic) {
		return string(diagnostic)
	}
	var usage usageError
	if errors.As(err, &usage) {
		return "invalid command or flags"
	}
	return failurelog.Cause(err)
}

// serverHTTP mirrors the session messaging CLI, including support for sessions
// launched before SERVER_HTTP was injected.
func serverHTTP() string {
	clean := func(key string) string {
		return strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/")
	}
	if base := clean("BLERG_RUNNER_SERVER_HTTP"); base != "" {
		return base
	}
	if preview := clean("BLERG_RUNNER_PREVIEW_URL"); strings.HasSuffix(preview, "/api/preview") {
		return strings.TrimRight(strings.TrimSuffix(preview, "/api/preview"), "/")
	}
	ws := clean("BLERG_RUNNER_SERVER_URL")
	ws = strings.TrimRight(strings.TrimSuffix(ws, "/ws/daemon"), "/")
	if strings.HasPrefix(ws, "ws://") {
		return "http://" + strings.TrimPrefix(ws, "ws://")
	}
	if strings.HasPrefix(ws, "wss://") {
		return "https://" + strings.TrimPrefix(ws, "wss://")
	}
	return ""
}
