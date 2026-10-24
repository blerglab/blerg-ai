package server

import (
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
			var matches []protocol.WorkspaceMember
			for _, m := range parent.Members {
				if m.ID == selector {
					matches = []protocol.WorkspaceMember{m}
					break
				}
				if m.Name == selector {
					matches = append(matches, m)
				}
			}
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
