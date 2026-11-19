package daemon

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

// Reads only the launch-bound repository, with the same pinning, vetting,
// limits and liveness checks as a commit read. Refs become full object IDs
// before either log or diff sees them. Never fetches or accesses a Git host.
func readWorkspaceBranch(parent context.Context, sid string, binding workspaceFileBinding, branch, base string, patch bool) protocol.CommitResult {
	fail := func(code string) protocol.CommitResult { return protocol.CommitResult{ErrorCode: code} }
	if !workbranch.Valid(branch) || base != "" && !workbranch.Valid(base) {
		return fail("invalid")
	}
	if binding.live == nil || binding.info == nil || !binding.live() {
		return fail("unavailable")
	}
	dir, trusted, ok := commitReadDir(sid, binding)
	if !ok {
		return fail("unavailable")
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail("unavailable")
	}
	tip, code := resolveRef(ctx, dir, "refs/heads/"+branch)
	if code != "" {
		return fail(code)
	}
	var root string
	if base != "" {
		root, code = resolveRef(ctx, dir, "refs/heads/"+base)
	} else {
		// The remote's recorded default is authoritative. Conventional local
		// defaults support repositories without a remote (including private work).
		for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/heads/main", "refs/heads/master"} {
			root, code = resolveRef(ctx, dir, ref)
			if code == "" {
				base = ref
				break
			}
		}
	}
	if code != "" {
		return fail("not_found")
	}
	merge := &cappedOutput{max: 256, strict: true}
	if dir.git(ctx, merge, "merge-base", root, tip) != nil {
		return fail("not_found")
	}
	root = strings.TrimSpace(merge.buf.String())
	if !protocol.ValidSessionOriginCommit(root) {
		return fail("unavailable")
	}
	var result protocol.CommitResult
	if patch {
		out := &cappedOutput{max: protocol.CommitPatchMaxBytes, strict: true}
		args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--submodule=short", "-O/dev/null", "-M", "-l1000", "--src-prefix=a/", "--dst-prefix=b/", root, tip, "--"}
		err := dir.git(ctx, out, args...)
		if out.over {
			return fail("too_large")
		}
		if err != nil {
			return fail("objects_unavailable")
		}
		result = protocol.CommitResult{OK: true, PatchB64: base64.StdEncoding.EncodeToString(out.buf.Bytes())}
	} else {
		out := &cappedOutput{max: 256 << 10, strict: true}
		if dir.git(ctx, out, "log", "-30", "--format=%H%x00%<(512,trunc)%s%x00%<(256,trunc)%an%x00%aI%x00", root+".."+tip, "--") != nil {
			return fail("unavailable")
		}
		info := &protocol.BranchInfo{Name: branch, Base: base, Commits: []protocol.BranchCommit{}}
		parts := strings.Split(out.buf.String(), "\x00")
		for len(parts) >= 4 {
			sha := strings.TrimSpace(parts[0])
			if !protocol.ValidSessionOriginCommit(sha) {
				return fail("unavailable")
			}
			msg, _ := capText(strings.TrimRight(parts[1], " "), protocol.CommitSubjectMaxBytes)
			author, _ := capText(strings.TrimRight(parts[2], " "), protocol.CommitAuthorMaxBytes)
			info.Commits = append(info.Commits, protocol.BranchCommit{SHA: sha, Message: msg, Author: author, Date: parts[3]})
			parts = parts[4:]
		}
		result = protocol.CommitResult{OK: true, Branch: info}
	}
	if ctx.Err() != nil || !binding.live() {
		return fail("unavailable")
	}
	return result
}
