package discovery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// ErrBindingPending means a daemon has not restored this launch's private
// selection yet. It grants no source access and is safe to retry after resume.
var ErrBindingPending = errors.New("binding_pending")

// CloneDestination checks the live workspace before any remote work. Folder
// workspaces receive the checkout inside their anchor; composed workspaces use
// the configured repositories root.
func (s *Service) CloneDestination(ctx context.Context, launch string) (string, error) {
	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.file.Close() }()
	s.mu.Lock()
	parent, ok := s.bound[launch]
	s.mu.Unlock()
	if !ok {
		return "", ErrBindingPending
	}
	if parent.rootName != root.name || parent.resolved.RootIdentity != root.tag {
		return "", errStale
	}
	if !recordAnchorCurrent(root, parent.selectionRecord, &operation{ctx: ctx}) {
		return "", errStale
	}
	anchor := "."
	rootMember := false
	for _, member := range parent.manifest.Members {
		rootMember = rootMember || member.Path == "."
	}
	if parent.physicalFolder && !rootMember {
		anchor = parent.anchorPath
	}
	dir, _, err := safeDirectory(root, anchor, &operation{ctx: ctx})
	if err != nil {
		return "", err
	}
	defer func() { _ = dir.Close() }()
	if !s.rootUnchanged(root) || ctx.Err() != nil {
		return "", errStale
	}
	return anchor, nil
}

// addition resolves one repository to join a live workspace. Discovery grants
// nothing: the server journals the result and the runtime pins it again while
// writers are fenced. The caller may omit the source identity only for Main's
// request, which a person still approves against the identity returned here.
func (s *Service) addition(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) (*protocol.DiscoveryAddition, error) {
	if len(req.Members) != 1 || req.Folder != nil || req.ParentLaunchGeneration == "" {
		return nil, errInvalid
	}
	if req.RootIdentity != "" && req.RootIdentity != root.tag {
		return nil, errStale
	}
	s.mu.Lock()
	parent, ok := s.bound[req.ParentLaunchGeneration]
	s.mu.Unlock()
	if !ok || parent.rootName != root.name || parent.resolved.RootIdentity != root.tag {
		return nil, errStale
	}
	ref := req.Members[0]
	rel, err := cleanPath(ref.Path)
	if err != nil {
		return nil, err
	}
	dir, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return nil, err
	}
	_ = dir.Close()
	if repo == nil || (ref.SourceIdentity != "" && repo.source.SourceIdentity != ref.SourceIdentity) {
		return nil, errStale
	}
	if err := validateSources(append(append([]protocol.DiscoverySource(nil), parent.resolved.Members...), repo.source)); err != nil {
		return nil, err
	}
	result := &protocol.DiscoveryAddition{Source: repo.source}
	// A folder that is itself the only repository takes the reserved folder, as
	// any workspace with a member at the root does. The server picks the name.
	rootMember := false
	for _, member := range parent.manifest.Members {
		rootMember = rootMember || member.Path == "."
	}
	if parent.physicalFolder && !rootMember {
		switch {
		case rel == parent.anchorPath:
			result.PhysicalPath = "."
		case parent.anchorPath == ".":
			result.PhysicalPath = rel
		case strings.HasPrefix(rel, parent.anchorPath+"/"):
			result.PhysicalPath = strings.TrimPrefix(rel, parent.anchorPath+"/")
		default:
			return nil, errOutside
		}
	}
	return result, nil
}

// PinAddition reopens an authorized addition without following links and proves
// it is still the journaled source. The caller holds writers fenced.
func (s *Service) PinAddition(ctx context.Context, launch string, ref protocol.SelectionRef, member protocol.WorkspaceMember) (*SourcePin, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.file.Close() }()
	s.mu.Lock()
	parent, ok := s.bound[launch]
	s.mu.Unlock()
	if !ok || parent.rootName != root.name || parent.resolved.RootIdentity != root.tag {
		return nil, errStale
	}
	rel, err := cleanPath(ref.Path)
	if err != nil {
		return nil, err
	}
	op := &operation{ctx: ctx}
	directory, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return nil, errStale
	}
	success := false
	defer func() {
		if !success {
			_ = directory.Close()
		}
	}()
	if repo == nil || repo.source.SourceIdentity != ref.SourceIdentity || repo.source.SourceIdentity != member.SourceIdentity || repo.source.ID != member.ID || repo.source.CommonDirectoryIdentity != member.CommonDirectoryIdentity || repo.source.Name != member.Name {
		return nil, errStale
	}
	// A linked worktree shares its common directory with a member already here.
	if validateSources(append(append([]protocol.DiscoverySource(nil), parent.resolved.Members...), repo.source)) != nil {
		return nil, errStale
	}
	common, err := openBeneath(root.file, repo.commonPath, directoryFlags)
	if err != nil {
		return nil, errStale
	}
	if !s.rootUnchanged(root) || ctx.Err() != nil {
		_ = common.Close()
		return nil, errStale
	}
	success = true
	return &SourcePin{Member: member, CanonicalPath: filepath.Join(root.name, filepath.FromSlash(rel)), CommonDirectoryPath: filepath.Join(root.name, filepath.FromSlash(repo.commonPath)), Directory: directory, CommonDirectory: common}, nil
}

// RebindMembership replaces a live launch's bound selection with its committed
// revision, so children select from current membership. The runtime proved each
// source; refs must hold exactly the manifest's members in order. Anchor and root
// never change across revisions.
func (s *Service) RebindMembership(ctx context.Context, m protocol.WorkspaceManifest, sources []string) error {
	if m.Validate() != nil || m.Digest != workspaceselection.Digest(m) || len(sources) != len(m.Members) {
		return errInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.file.Close() }()
	s.mu.Lock()
	old, ok := s.bound[m.LaunchGeneration]
	s.mu.Unlock()
	if !ok || old.rootName != root.name {
		return errStale
	}
	op := &operation{ctx: ctx}
	record := old.selectionRecord
	record.refs = make([]protocol.SelectionRef, 0, len(sources))
	members := make([]protocol.DiscoverySource, 0, len(sources))
	for i, source := range sources {
		rel, err := filepath.Rel(root.name, filepath.Clean(source))
		if err != nil || !filepath.IsAbs(source) {
			return errInvalid
		}
		rel, err = cleanPath(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		dir, repo, err := safeDirectory(root, rel, op)
		if err != nil {
			return err
		}
		_ = dir.Close()
		if repo == nil || repo.source.ID != m.Members[i].ID || repo.source.SourceIdentity != m.Members[i].SourceIdentity || repo.source.CommonDirectoryIdentity != m.Members[i].CommonDirectoryIdentity {
			return errStale
		}
		record.refs = append(record.refs, protocol.SelectionRef{Path: rel, SourceIdentity: repo.source.SourceIdentity})
		members = append(members, repo.source)
	}
	record.resolved = copyResolved(old.resolved)
	record.resolved.Members = members
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bound[m.LaunchGeneration]; !ok || current.manifest.LaunchGeneration != old.manifest.LaunchGeneration {
		return errStale
	}
	s.bound[m.LaunchGeneration] = boundRecord{selectionRecord: record, manifest: m}
	return nil
}
