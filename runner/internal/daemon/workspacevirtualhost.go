package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// HostWorkspaceSource is private trusted selection state, never serialized to a
// browser. The canonical source identity was pinned by P1 before launch.
type HostWorkspaceSource struct {
	Path string
	Root protocol.WorkspaceRoot
}

// VirtualHostExplorer projects only selected sources at their immutable member
// paths. Its namespace root is an empty private allocation pinned by the runtime;
// neither it nor a common filesystem ancestor supplies visible file names.
type VirtualHostExplorer struct {
	namespace *LocalExplorer
	members   map[string]*LocalExplorer
	manifest  protocol.WorkspaceManifest
	statusMu  sync.Mutex
}

func NewVirtualHostExplorer(namespaceRoot string, manifest protocol.WorkspaceManifest, binding protocol.WorkspaceRuntimeBinding, sources map[string]HostWorkspaceSource, live func() bool) (*VirtualHostExplorer, error) {
	if manifest.Runtime != "host" || len(sources) != len(manifest.Members) {
		return nil, os.ErrPermission
	}
	namespace, err := NewLocalExplorer(namespaceRoot, manifest, binding, live)
	if err != nil {
		return nil, err
	}
	virtual := &VirtualHostExplorer{namespace: namespace, members: map[string]*LocalExplorer{}, manifest: namespace.manifest}
	fail := func() (*VirtualHostExplorer, error) { _ = virtual.Close(); return nil, os.ErrPermission }
	for _, member := range manifest.Members {
		source, ok := sources[member.ID]
		_, public := explorerPath(member.Path)
		if !ok || !public || member.Path == "." {
			return fail()
		}
		info, err := os.Lstat(source.Path)
		if err != nil || !info.IsDir() || workspaceRootIdentity(info) != source.Root {
			return fail()
		}
		localManifest := manifest
		localMember := member
		localMember.Path = "."
		localManifest.Members = []protocol.WorkspaceMember{localMember}
		local, err := newLocalExplorer(source.Path, info, localManifest, binding.Generation, live)
		if err != nil {
			return fail()
		}
		virtual.members[member.ID] = local
	}
	return virtual, nil
}
func (v *VirtualHostExplorer) Close() error {
	for _, member := range v.members {
		_ = member.Close()
	}
	return v.namespace.Close()
}
func (v *VirtualHostExplorer) memberFor(name string) (protocol.WorkspaceMember, *LocalExplorer, string) {
	for _, member := range v.manifest.Members {
		if name == member.Path {
			return member, v.members[member.ID], ""
		}
		if strings.HasPrefix(name, member.Path+"/") {
			return member, v.members[member.ID], strings.TrimPrefix(name, member.Path+"/")
		}
	}
	return protocol.WorkspaceMember{}, nil, ""
}
func (v *VirtualHostExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	result := explorerFailure(req, "unavailable")
	if req.LaunchGeneration != v.manifest.LaunchGeneration || req.Generation != v.namespace.generation || !v.namespace.valid() {
		return result
	}
	name, ok := explorerPath(req.Path)
	if !ok || len(req.Filter) > 256 || !utf8.ValidString(req.Filter) || len(req.Cursor) > 8192 {
		return explorerFailure(req, "forbidden")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if req.Operation == "status" {
		if !v.statusMu.TryLock() {
			return explorerFailure(req, "busy")
		}
		defer v.statusMu.Unlock()
	}
	member, local, relative := v.memberFor(name)
	if local != nil {
		if req.MemberID != "" && req.MemberID != member.ID {
			return explorerFailure(req, "forbidden")
		}
		inner := req
		inner.Path = relative
		result = local.Explore(ctx, inner)
		result.Path = req.Path
		for i := range result.Entries {
			result.Entries[i].Path = path.Join(member.Path, result.Entries[i].Path)
		}
		if result.Status != nil {
			status := *result.Status
			status.Path = req.Path
			result.Status = &status
		}
	} else {
		switch req.Operation {
		case "list":
			result = v.list(req, name)
		case "search":
			result = v.search(ctx, req, name)
		default:
			result = explorerFailure(req, "forbidden")
		}
	}
	if !v.namespace.valid() {
		return explorerFailure(req, "unavailable")
	}
	if ctx.Err() != nil {
		if req.Operation != "search" || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return explorerFailure(req, "unavailable")
		}
		if result.OK {
			result.Truncated = true
		}
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > protocol.WorkspaceExplorerReplyBytes {
		return explorerFailure(req, "unavailable")
	}
	return result
}
func (v *VirtualHostExplorer) list(req protocol.WorkspaceExplorerRequest, name string) protocol.WorkspaceExplorerResult {
	result := explorerFailure(req, "not_found")
	if req.Cursor != "" {
		return explorerFailure(req, "stale")
	}
	rows := map[string]protocol.WorkspaceEntry{}
	known := name == "."
	for _, member := range v.manifest.Members {
		remainder := member.Path
		if name != "." {
			if !strings.HasPrefix(remainder, name+"/") {
				continue
			}
			remainder = strings.TrimPrefix(remainder, name+"/")
		}
		known = true
		first, _, _ := strings.Cut(remainder, "/")
		child := path.Join(name, first)
		row := protocol.WorkspaceEntry{Name: first, Path: child, Directory: true}
		if child == member.Path {
			row.Repository = true
			row.MemberID = member.ID
		}
		if !strings.Contains(strings.ToLower(first), strings.ToLower(req.Filter)) {
			continue
		}
		rows[child] = row
	}
	for _, row := range rows {
		result.Entries = append(result.Entries, row)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Name < result.Entries[j].Name })
	if !known {
		return result
	}
	for _, member := range v.manifest.Members {
		if !v.members[member.ID].valid() {
			result.UnavailableMembers = append(result.UnavailableMembers, member.ID)
		}
	}
	result.OK = true
	result.ErrorCode = ""
	return result
}
func (v *VirtualHostExplorer) search(ctx context.Context, req protocol.WorkspaceExplorerRequest, name string) protocol.WorkspaceExplorerResult {
	result := explorerFailure(req, "invalid")
	if strings.TrimSpace(req.Filter) == "" || len(req.Filter) > 256 || req.Cursor != "" {
		return result
	}
	ctx = workspaceIgnoreRequest(ctx)
	budget := protocol.WorkspaceSearchVisited
	bytes := 0
	byteBudget := v.namespace.entryReplyBudget(req)
	for _, member := range v.manifest.Members {
		if name != "." && !strings.HasPrefix(member.Path, name+"/") {
			continue
		}
		local := v.members[member.ID]
		if !local.valid() {
			result.UnavailableMembers = append(result.UnavailableMembers, member.ID)
			continue
		}
		if strings.Contains(strings.ToLower(member.Name), strings.ToLower(req.Filter)) {
			result.Entries = append(result.Entries, protocol.WorkspaceEntry{Name: path.Base(member.Path), Path: member.Path, Directory: true, Repository: true, MemberID: member.ID})
		}
		offset := len(strings.Split(member.Path, "/"))
		if name != "." {
			offset -= len(strings.Split(name, "/"))
		}
		inner := req
		inner.Path = ""
		sub := local.searchWithBudget(ctx, inner, ".", &budget, protocol.WorkspaceSearchHits-len(result.Entries), max(0, byteBudget-bytes), offset)
		if !sub.OK || !local.valid() {
			return explorerFailure(req, "unavailable")
		}
		for _, row := range sub.Entries {
			row.Path = path.Join(member.Path, row.Path)
			raw, _ := json.Marshal(row)
			if bytes+len(raw) > byteBudget {
				result.Truncated = true
				result.OK = true
				result.ErrorCode = ""
				return result
			}
			bytes += len(raw)
			result.Entries = append(result.Entries, row)
		}
		result.Truncated = result.Truncated || sub.Truncated
		if ctx.Err() != nil || budget <= 0 || len(result.Entries) >= protocol.WorkspaceSearchHits {
			result.Truncated = true
			break
		}
	}
	result.OK = true
	result.ErrorCode = ""
	return result
}
func (v *VirtualHostExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	failed := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	if !v.namespace.valid() {
		return failed
	}
	clean, ok := workspaceCleanRequest(req.Path)
	if !ok || !workspacePublicPath(clean) {
		return failed
	}
	_, local, relative := v.memberFor(clean)
	if local == nil || relative == "" {
		return failed
	}
	inner := req
	inner.Path = relative
	result := local.ReadFile(ctx, inner)
	result.Path = clean
	if !v.namespace.valid() {
		return failed
	}
	return result
}
