package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var boardBodyCard = regexp.MustCompile(`\bBLG-([1-9][0-9]{0,6})\b`)

func readBoardHistory(parent context.Context, req protocol.ReadCommit, binding workspaceFileBinding) protocol.CommitResult {
	fail := func(cause string) protocol.CommitResult {
		failurelog.Default.Event("board_history:"+cause, slog.LevelWarn, "component", "daemon", "operation", "board_history", "cause", cause)
		return protocol.CommitResult{ErrorCode: cause}
	}
	if binding.live == nil || binding.info == nil || !binding.live() {
		return fail("binding_unavailable")
	}
	dir, trusted, ok := commitReadDir(req.SessionID, binding)
	if !ok {
		return fail("repository_unavailable")
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail("repository_untrusted")
	}
	tip, code := resolveRef(ctx, dir, "refs/heads/main")
	if code != "" {
		return fail("main_unavailable")
	}
	deployed := map[string]bool{}
	if req.SHA != "" {
		if !protocol.ValidCommitLookupSHA(req.SHA) {
			return fail("invalid_deploy")
		}
		sha, code := resolveRef(ctx, dir, req.SHA)
		if code != "" {
			return fail("deploy_unavailable")
		}
		out := &cappedOutput{max: 1 << 20, strict: true}
		if dir.git(ctx, out, "rev-list", "--max-count=10001", sha, "--") != nil {
			return fail("deploy_history_failed")
		}
		for _, s := range strings.Fields(out.buf.String()) {
			deployed[s] = true
		}
		if len(deployed) > 10000 {
			return fail("deploy_history_limit")
		}
	}
	out := &cappedOutput{max: 8 << 20, strict: true}
	if dir.git(ctx, out, "log", "--topo-order", "--max-count=10001", "--format=%H%x00%cI%x00%P%x00%s%x00%b%x00", tip, "--") != nil {
		return fail("history_failed")
	}
	fields := strings.Split(out.buf.String(), "\x00")
	history := &protocol.CodeHistory{Tip: tip}
	for len(fields) >= 6 {
		sha := strings.TrimSpace(fields[0])
		date := fields[1]
		if !protocol.ValidSessionOriginCommit(sha) {
			return fail("invalid_history")
		}
		if _, err := time.Parse(time.RFC3339, date); err != nil {
			return fail("invalid_history")
		}
		c := protocol.CodeCommit{SHA: sha, Date: date, Parents: strings.Fields(fields[2]), Subject: fields[3], Deployed: deployed[sha]}
		if len(c.Parents) > 1 {
			for _, match := range boardBodyCard.FindAllStringSubmatch(fields[4], -1) {
				n, _ := strconv.Atoi(match[1])
				if !slices.Contains(c.BodyCards, n) {
					c.BodyCards = append(c.BodyCards, n)
				}
			}
		}
		for _, line := range strings.Split(fields[4], "\n") {
			if s, ok := strings.CutPrefix(line, "This reverts commit "); ok {
				if words := strings.Fields(s); len(words) > 0 {
					s = words[0]
				}
				s = strings.TrimRight(s, ".,")
				if protocol.ValidSessionOriginCommit(s) {
					c.Reverts = s
				}
			}
		}
		history.Commits = append(history.Commits, c)
		fields = fields[5:]
		if len(history.Commits) > 10000 {
			return fail("history_limit")
		}
	}
	if req.BoardActivitySince != "" {
		since, err := time.Parse(time.RFC3339, req.BoardActivitySince)
		if err != nil {
			return fail("invalid_activity_since")
		}
		activity := &cappedOutput{max: 1 << 20, strict: true}
		if dir.git(ctx, activity, "log", "--branches", "--since="+since.UTC().Format(time.RFC3339), "--max-count=1001", "--format=%cI%x00%s%x00", "--") != nil {
			return fail("activity_history_failed")
		}
		parts := strings.Split(activity.buf.String(), "\x00")
		for len(parts) >= 3 {
			date := strings.TrimSpace(parts[0])
			subject := parts[1]
			if _, err := time.Parse(time.RFC3339, date); err != nil {
				return fail("invalid_activity_history")
			}
			if len(subject) > 4096 {
				return fail("activity_subject_limit")
			}
			history.Activity = append(history.Activity, protocol.CodeActivity{Date: date, Subject: subject})
			if len(history.Activity) > 1000 {
				return fail("activity_history_limit")
			}
			parts = parts[2:]
		}
		history.ActivityComplete = true
	}
	if !binding.live() || ctx.Err() != nil {
		return fail("read_canceled")
	}
	encoded, err := json.Marshal(history)
	if err != nil || len(encoded) > 2<<20 {
		return fail("history_frame_limit")
	}
	return protocol.CommitResult{OK: true, CodeHistory: history}
}
