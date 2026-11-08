package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Read only already delivered objects through the pinned, vetted Git path. No
// patch or body is retained, and the entire range shares one output budget.
func deliveredSummary(ctx context.Context, dir *pinnedGitDir, base, head string) *protocol.DeliveredSummary {
	if base == head || !protocol.ValidSessionOriginCommit(base) || !protocol.ValidSessionOriginCommit(head) {
		return nil
	}
	out := &cappedOutput{max: 512 << 10, strict: true}
	if dir.git(ctx, out, "log", "--max-count=101", "--no-color", "--no-notes", "--no-show-signature", "--no-mailmap", "--encoding=UTF-8", "--format=%H%x00%<(256,trunc)%an%x00%aI%x00%<(512,trunc)%s", base+".."+head, "--") != nil || out.over {
		return nil
	}
	s := &protocol.DeliveredSummary{Commits: []protocol.BranchCommit{}, Files: []protocol.CommitFile{}}
	for _, line := range strings.Split(strings.TrimSpace(out.buf.String()), "\n") {
		p := strings.Split(line, "\x00")
		if len(p) != 4 || !protocol.ValidSessionOriginCommit(p[0]) {
			return nil
		}
		if len(s.Commits) == 100 {
			s.Truncated = true
			break
		}
		author, _ := capText(strings.TrimRight(p[1], " "), protocol.CommitAuthorMaxBytes)
		subject, _ := capText(strings.TrimRight(p[3], " "), protocol.CommitSubjectMaxBytes)
		s.Commits = append(s.Commits, protocol.BranchCommit{SHA: p[0], Author: author, Date: p[2], Message: subject})
	}
	if len(s.Commits) == 0 {
		return nil
	}
	stat := &cappedOutput{max: commitNumstatMaxBytes}
	args := append(append([]string{"diff", "--numstat", "-z"}, []string{"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--submodule=short", "-O/dev/null", "-M", "-l1000"}...), base, head, "--")
	if dir.git(ctx, stat, args...) != nil {
		return nil
	}
	var filesTruncated bool
	s.Files, _, _, filesTruncated = parseNumstat(stat.buf.Bytes(), stat.over)
	s.Truncated = s.Truncated || filesTruncated
	for {
		raw, _ := json.Marshal(s)
		if len(raw) <= 96<<10 {
			break
		}
		s.Truncated = true
		switch {
		case len(s.Files) > 0:
			s.Files = s.Files[:len(s.Files)-1]
		case len(s.Commits) > 0:
			s.Commits = s.Commits[:len(s.Commits)-1]
		default:
			return nil
		}
	}
	// Drop unsafe metadata rather than changing workspace settlement success.
	if !s.Valid() {
		return nil
	}
	return s
}
