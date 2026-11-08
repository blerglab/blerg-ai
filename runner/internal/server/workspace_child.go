package server

import (
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"net/http"
	"strings"
)

// Selection is restricted to the immutable parent manifest. Names are accepted
// only when unambiguous; stable member IDs always work. Runtime paths survive
// subset selection, including a single member of a multi-repository parent.
func selectWorkspaceChild(cs *childStart, parent protocol.WorkspaceManifest) *APIError {
	if parent.Runtime == "host" && parent.Kind == "folder" && len(parent.Members) == 0 {
		return apiErrorf(http.StatusUnprocessableEntity, "This session was started in a folder with no Git repository, so it can't start host children. Stop it and start a new session on the repository.")
	}
	fail := func() *APIError {
		return apiErrorf(http.StatusUnprocessableEntity, "select parent workspace members with repeatable --repo <member> or --all-repos; keep the parent's runtime")
	}
	if parent.Validate() != nil || cs.NoRepo || (cs.AllRepos && (cs.Repo != "" || len(cs.Repos) > 0)) || (cs.Repo != "" && len(cs.Repos) > 0) {
		return fail()
	}
	runtime := map[string]string{"host": "daemon", "sandbox": "docker", "cluster": "cluster"}[parent.Runtime]
	if cs.Runtime != "" && cs.Runtime != runtime {
		return fail()
	}
	cs.Runtime = runtime
	selectors := append([]string(nil), cs.Repos...)
	if cs.Repo != "" {
		selectors = append(selectors, cs.Repo)
	}
	if !cs.AllRepos && len(selectors) == 0 {
		return fail()
	}
	child := parent
	child.Members = []protocol.WorkspaceMember{}
	child.LaunchGeneration = newUUID()
	if cs.AllRepos {
		child.Members = append(child.Members, parent.Members...)
	} else {
		seen := map[string]bool{}
		for _, selector := range selectors {
			matches := workspaceSelectorMatches(parent.Members, selector)
			if len(matches) != 1 || seen[matches[0].ID] {
				return fail()
			}
			seen[matches[0].ID] = true
			child.Members = append(child.Members, matches[0])
		}
	}
	child.Digest = workspaceselection.Digest(child)
	if protocol.ValidateWorkspaceChild(parent, child) != nil {
		return fail()
	}
	cs.Workspace = &child
	return nil
}

// selectHostClusterChildSelectors is selectWorkspaceChild's sibling for BLG-501 (design §3):
// a host Main's explicit --runtime cluster child. Taken only when parent.Runtime == "host"
// and cs.Runtime == "cluster" is already explicit (never inferred) — the caller checks this
// before calling in, same as selectWorkspaceChild's own runtime-equality pin.
//
// This is the pure, no-network half only: member-subset selection, canonicalized to parent
// order, and the required --base. It never sets cs.Workspace (a host→cluster launch is not
// bound to the desktop daemon's fence the way an ordinary host/sandbox child is — §5,
// location independence) and never resolves a member's provider repository or credential;
// a.hostClusterChildWorkspace does that once a session row exists to authorize it against.
func selectHostClusterChildSelectors(cs *childStart, parent protocol.WorkspaceManifest) *APIError {
	fail := func() *APIError {
		return apiErrorf(http.StatusUnprocessableEntity, "select parent workspace members with repeatable --repo <member> or --all-repos, with --runtime cluster and --base <branch>")
	}
	if parent.Validate() != nil || parent.Runtime != "host" || cs.Runtime != runnerRuntimeCluster || cs.NoRepo || (cs.AllRepos && (cs.Repo != "" || len(cs.Repos) > 0)) || (cs.Repo != "" && len(cs.Repos) > 0) {
		return fail()
	}
	if !validBaseBranch(cs.Base) {
		return apiErrorf(http.StatusUnprocessableEntity, "a cluster worker from a desktop Main needs an explicit --base <branch> (e.g. alpha/<release>); there is no default-branch fallback")
	}
	selectors := append([]string(nil), cs.Repos...)
	if cs.Repo != "" {
		selectors = append(selectors, cs.Repo)
	}
	if !cs.AllRepos && len(selectors) == 0 {
		return fail()
	}
	seen := map[string]bool{}
	if cs.AllRepos {
		for _, m := range parent.Members {
			seen[m.ID] = true
		}
	} else {
		for _, selector := range selectors {
			matches := workspaceSelectorMatches(parent.Members, selector)
			if len(matches) != 1 || seen[matches[0].ID] {
				return fail()
			}
			seen[matches[0].ID] = true
		}
	}
	// Canonical parent order, matching selectClusterChild's own canonicalization — the
	// server's own record decides order, never the caller's selector list.
	ordered := make([]string, 0, len(seen))
	for _, m := range parent.Members {
		if seen[m.ID] {
			ordered = append(ordered, m.ID)
		}
	}
	if len(ordered) == 0 {
		return fail()
	}
	cs.Repos, cs.Repo, cs.Provider, cs.AllRepos, cs.WorkspaceLaunch = ordered, "", "", false, parent.LaunchGeneration
	return nil
}

// validBaseBranch is a shape check only, not a provider lookup (that happens later, in
// a.hostClusterChildWorkspace's checkBaseBranchPushed). It exists so an obviously malformed
// value never reaches a git clone's --branch=<value> argument or a provider HTTP path: no
// leading '-' (never a flag, even via "=" form, which this increment relies on but doesn't
// want as the only guard), no control characters, no '..', and a bounded length.
func validBaseBranch(branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.Contains(branch, "..") {
		return false
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" {
			return false
		}
	}
	return true
}

func childWorkspaceMembers(cs childStart) []protocol.WorkspaceMember {
	if cs.Workspace == nil {
		return nil
	}
	return cs.Workspace.Members
}

func workspaceMemberKeys(m *protocol.WorkspaceManifest) []string {
	if m == nil {
		return nil
	}
	out := []string{m.OwnerID, m.Runtime}
	for _, v := range m.Members {
		out = append(out, v.ID+":"+v.SourceIdentity+":"+v.Path)
	}
	return out
}

// workspacePrompt reproduces the old launch suffix for replay and retry compatibility.
// Agent adapters now supply membership as engine instructions instead.
func workspacePrompt(prompt string, m *protocol.WorkspaceManifest) string {
	if m == nil || strings.TrimSpace(prompt) == "" {
		return prompt
	}
	var guide strings.Builder
	guide.WriteString("\n\nWorkspace repositories (immutable selection; paths relative to the runtime workspace):\n")
	for _, member := range m.Members {
		fmt.Fprintf(&guide, "- %s: %s — %s\n", member.ID, member.Name, member.Path)
	}
	guide.WriteString("Work in the affected member path. Commit each changed repository separately and report every commit ID. This selection grants no shell permission. Newly discovered repositories are not launch members.\n")
	return prompt + guide.String()
}

// Stable IDs win, then exact projected paths, then unambiguous display names.
func workspaceSelectorMatches(members []protocol.WorkspaceMember, selector string) []protocol.WorkspaceMember {
	for _, member := range members {
		if member.ID == selector {
			return []protocol.WorkspaceMember{member}
		}
	}
	for _, member := range members {
		if member.Path == selector {
			return []protocol.WorkspaceMember{member}
		}
	}
	var matches []protocol.WorkspaceMember
	for _, member := range members {
		if member.Name == selector {
			matches = append(matches, member)
		}
	}
	return matches
}

// Only remove the exact generated suffix for the session's trusted membership.
// Keep the stored event intact: recovery still has its original model context.
func cleanLegacyWorkspaceMessage(raw json.RawMessage, m *protocol.WorkspaceManifest) json.RawMessage {
	if m == nil {
		return raw
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return raw
	}
	var text string
	if json.Unmarshal(payload["text"], &text) != nil {
		return raw
	}
	suffix := strings.TrimPrefix(workspacePrompt("prompt", m), "prompt")
	if !strings.HasSuffix(text, suffix) {
		return raw
	}
	payload["text"], _ = json.Marshal(strings.TrimSuffix(text, suffix))
	out, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return out
}
