package daemon

import (
	"context"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Read the one authorized member. All commands use the config-isolated hostgit
// runner; no checkout, status, fetch, hooks, or worker filesystem reads.
func readBranchGraph(parent context.Context, sid string, binding workspaceFileBinding, depth int) protocol.CommitResult {
	fail := func() protocol.CommitResult { return protocol.CommitResult{ErrorCode: "unavailable"} }
	if depth < 1 || depth > 200 || binding.live == nil || binding.info == nil || !binding.live() {
		return fail()
	}
	dir, trusted, ok := commitReadDir(sid, binding)
	if !ok {
		return fail()
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail()
	}
	refs := &cappedOutput{max: 64 << 10, strict: true}
	graph := &protocol.BranchGraph{Refs: []protocol.GraphRef{}, Commits: []protocol.GraphCommit{}}
	if dir.git(ctx, refs, "for-each-ref", "--sort=-committerdate", "--count=65", "--format=%(objectname) %(objecttype) %(refname:strip=2)", "refs/heads/") != nil {
		// A missing object makes ref-filter's date sort fail. Recover a bounded
		// healthy subset instead of hiding every branch because of one bad ref.
		refs = &cappedOutput{max: 64 << 10, strict: true}
		if dir.git(ctx, refs, "for-each-ref", "--count=65", "--format=%(objectname) commit %(refname:strip=2)", "refs/heads/") != nil {
			return fail()
		}
		graph.RefsLimited = true
	}
	// Prefer the local default branch recorded by origin, then the conventional
	// names, then HEAD for repositories without remote metadata.
	defaultName := ""
	remote := &cappedOutput{max: 2048, strict: true}
	if dir.git(ctx, remote, "symbolic-ref", "-q", "refs/remotes/origin/HEAD") == nil {
		defaultName = strings.TrimPrefix(strings.TrimSpace(remote.buf.String()), "refs/remotes/origin/")
	}
	for _, name := range []string{defaultName, "main", "master", "HEAD"} {
		if name == "" {
			continue
		}
		ref := "refs/heads/" + name
		if name == "HEAD" {
			ref = "HEAD"
		}
		sha, code := resolveRef(ctx, dir, ref)
		if code != "" {
			continue
		}
		if name == "HEAD" {
			head := &cappedOutput{max: 2048, strict: true}
			if dir.git(ctx, head, "symbolic-ref", "-q", "--short", "HEAD") != nil {
				continue
			}
			name = strings.TrimSpace(head.buf.String())
		}
		graph.DefaultBranch = name
		graph.Refs = append(graph.Refs, protocol.GraphRef{Name: name, SHA: sha})
		break
	}
	if graph.DefaultBranch != "main" {
		if sha, code := resolveRef(ctx, dir, "refs/heads/main"); code == "" {
			graph.Refs = append(graph.Refs, protocol.GraphRef{Name: "main", SHA: sha})
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(refs.buf.String()), "\n") {
		if line == "" {
			continue
		}
		sha, rest, ok := strings.Cut(line, " ")
		kind, name, typed := strings.Cut(rest, " ")
		if !ok || !typed || kind != "commit" || !protocol.ValidSessionOriginCommit(sha) || len(name) > 1024 {
			continue
		}
		if graph.RefsLimited {
			if _, code := resolveRef(ctx, dir, sha); code != "" {
				continue
			}
		}
		if name == graph.DefaultBranch || name == "main" {
			continue
		}
		if len(graph.Refs) == 64 {
			graph.RefsLimited = true
			break
		}
		graph.Refs = append(graph.Refs, protocol.GraphRef{Name: name, SHA: sha})

	}
	// Tags are discovered locally, never supplied by a client. Peel annotated
	// tags in ref-filter and keep only commit objects, under a separate cap.
	tags := &cappedOutput{max: 64 << 10, strict: true}
	if dir.git(ctx, tags, "for-each-ref", "--sort=-creatordate", "--count=17", "--format=%(objectname) %(objecttype) %(*objectname) %(*objecttype) %(refname:strip=2)", "refs/tags/") != nil {
		return fail()
	}
	for _, line := range strings.Split(strings.TrimSpace(tags.buf.String()), "\n") {
		fields := strings.SplitN(line, " ", 5)
		if len(fields) != 5 {
			continue
		}
		sha, kind := fields[0], fields[1]
		if kind == "tag" {
			sha, kind = fields[2], fields[3]
		}
		if kind != "commit" || !protocol.ValidSessionOriginCommit(sha) || fields[4] == "" || len(fields[4]) > 1024 {
			continue
		}
		if len(graph.Tags) == 16 {
			graph.TagsLimited = true
			break
		}
		graph.Tags = append(graph.Tags, protocol.GraphTag{GraphRef: protocol.GraphRef{Name: fields[4], SHA: sha}})
	}

	if len(graph.Refs) == 0 && len(graph.Tags) == 0 {
		return protocol.CommitResult{OK: true, Graph: graph}
	}
	// Git's default date traversal stops at the requested depth. --topo-order
	// and --date-order both sort the reachable history before emitting rows.
	args := []string{"log", "--ignore-missing", "--no-color", "--no-notes", "--no-show-signature", "--no-mailmap", "--encoding=UTF-8", "--format=%H%x00%P%x00%<(512,trunc)%s%x00%cI%x00", "-" + strconv.Itoa((depth+1)*(len(graph.Refs)+len(graph.Tags)))}
	for _, ref := range graph.Refs {
		args = append(args, ref.SHA)
	}
	for _, tag := range graph.Tags {
		args = append(args, tag.SHA)
	}
	args = append(args, "--")
	out := &cappedOutput{max: 16 << 20, strict: true}
	if dir.git(ctx, out, args...) != nil {
		return fail()
	}
	parts := strings.Split(out.buf.String(), "\x00")
	for len(parts) >= 4 {
		sha := strings.TrimSpace(parts[0])
		if !protocol.ValidSessionOriginCommit(sha) {
			return fail()
		}
		parents := strings.Fields(parts[1])
		if len(parents) > protocol.CommitParentsMax {
			return fail()
		}
		for _, p := range parents {
			if !protocol.ValidSessionOriginCommit(p) {
				return fail()
			}
		}
		subject, _ := capText(strings.TrimRight(parts[2], " "), protocol.CommitSubjectMaxBytes)
		graph.Commits = append(graph.Commits, protocol.GraphCommit{SHA: sha, Parents: append([]string{}, parents...), Subject: subject, Date: strings.TrimSpace(parts[3])})
		parts = parts[4:]
	}
	graph.Commits = orderGraphCommits(graph.Commits)
	if len(graph.Commits) > depth {
		graph.More = true
		graph.Commits = graph.Commits[:depth]
	}
	populateGraphTags(graph)
	if ctx.Err() != nil || !binding.live() {
		return fail()
	}
	return protocol.CommitResult{OK: true, Graph: graph}
}

// Sort only the bounded window, keeping children above their parents even when
// several tips share a timestamp. Git must never sort all reachable history.
func orderGraphCommits(commits []protocol.GraphCommit) []protocol.GraphCommit {
	byID := make(map[string]protocol.GraphCommit, len(commits))
	for _, c := range commits {
		byID[c.SHA] = c
	}
	seen := make(map[string]bool, len(commits))
	ordered := make([]protocol.GraphCommit, 0, len(commits))
	var visit func(string)
	visit = func(id string) {
		c, ok := byID[id]
		if !ok || seen[id] {
			return
		}
		seen[id] = true
		for _, p := range c.Parents {
			visit(p)
		}
		ordered = append(ordered, c)
	}
	for i := len(commits) - 1; i >= 0; i-- {
		visit(commits[i].SHA)
	}
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	return ordered
}

// Ancestry proof is confined to the displayed window. Missing parents mean
// unknown, not exclusion. This never performs an unbounded merge-base walk.
func populateGraphTags(graph *protocol.BranchGraph) {
	byID := make(map[string]protocol.GraphCommit, len(graph.Commits))
	for _, c := range graph.Commits {
		byID[c.SHA] = c
	}
	for i := range graph.Tags {
		tag := &graph.Tags[i]
		tag.Contains = []string{}
		tag.Complete = true
		seen := map[string]bool{}
		queue := []string{tag.SHA}
		for len(queue) > 0 {
			sha := queue[0]
			queue = queue[1:]
			if seen[sha] {
				continue
			}
			seen[sha] = true
			c, ok := byID[sha]
			if !ok {
				tag.Complete = false
				continue
			}
			tag.Contains = append(tag.Contains, sha)
			if tag.Previous == "" && sha != tag.SHA {
				for _, other := range graph.Tags {
					if other.SHA == sha {
						tag.Previous = other.Name
						break
					}
				}
			}
			queue = append(queue, c.Parents...)
		}
	}
}
