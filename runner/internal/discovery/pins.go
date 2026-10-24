package discovery

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// SourcePin is private runtime preparation input. No file path or descriptor
// may enter a browser payload, agent file or public workspace manifest.
type SourcePin struct {
	Member              protocol.WorkspaceMember `json:"-"`
	CanonicalPath       string                   `json:"-"`
	CommonDirectoryPath string                   `json:"-"`
	Directory           *os.File                 `json:"-"`
	CommonDirectory     *os.File                 `json:"-"`
}
type BoundPins struct {
	Manifest       protocol.WorkspaceManifest `json:"-"`
	ConfiguredRoot *os.File                   `json:"-"`
	Anchor         *os.File                   `json:"-"`
	AnchorPath     string                     `json:"-"`
	Sources        []SourcePin                `json:"-"`
}

func (pins *BoundPins) Close() {
	if pins.ConfiguredRoot != nil {
		_ = pins.ConfiguredRoot.Close()
	}
	if pins.Anchor != nil {
		_ = pins.Anchor.Close()
	}
	for _, source := range pins.Sources {
		if source.Directory != nil {
			_ = source.Directory.Close()
		}
		if source.CommonDirectory != nil {
			_ = source.CommonDirectory.Close()
		}
	}
}

// PinBound is the P1 selection service consumed by runtime preparers. It
// accepts only a manifest previously bound by the authenticated server, then
// reopens the root, source and common directories without following links.
// P2 owns cloning/preparation and persists the returned private identities;
// neither file authority nor shell permission is conferred by this method.
func (s *Service) PinBound(ctx context.Context, m protocol.WorkspaceManifest) (*BoundPins, error) {
	s.mu.Lock()
	record, ok := s.bound[m.LaunchGeneration]
	s.mu.Unlock()
	if !ok || m.Validate() != nil || m.Digest != workspaceselection.Digest(m) || record.manifest.Digest != m.Digest {
		return nil, errStale
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	record.manifest.Members = append([]protocol.WorkspaceMember(nil), record.manifest.Members...)
	pins := &BoundPins{Manifest: record.manifest, ConfiguredRoot: root.file, Sources: []SourcePin{}}
	success := false
	defer func() {
		if !success {
			pins.Close()
		}
	}()
	if root.tag != record.resolved.RootIdentity || record.rootName != root.name {
		return nil, errStale
	}
	op := &operation{ctx: ctx}
	current := record.selectionRecord
	if len(record.refs) > 0 || record.resolved.Folder != nil {
		current, err = s.resolve(root, protocol.DiscoverWorkspace{Operation: "resolve", RootIdentity: record.resolved.RootIdentity, Members: record.refs, Folder: record.resolved.Folder, MembershipSnapshot: record.resolved.MembershipSnapshot, Runtime: record.runtime}, op)
		if err != nil || current.resolved.SelectionIdentity != record.resolved.SelectionIdentity {
			return nil, errStale
		}
	}
	if !recordAnchorCurrent(root, record.selectionRecord, op) {
		return nil, errStale
	}
	anchorPath := record.anchorPath
	pins.Anchor, _, err = safeDirectory(root, anchorPath, op)
	if err != nil {
		return nil, err
	}
	pins.AnchorPath = filepath.Join(root.name, filepath.FromSlash(anchorPath))
	for i, source := range current.resolved.Members {
		directory, repo, err := safeDirectory(root, source.Path, op)
		if err != nil || repo == nil {
			if directory != nil {
				_ = directory.Close()
			}
			return nil, errStale
		}
		common, err := openBeneath(root.file, repo.commonPath, directoryFlags)
		if err != nil {
			_ = directory.Close()
			return nil, errStale
		}
		pins.Sources = append(pins.Sources, SourcePin{Member: record.manifest.Members[i], CanonicalPath: filepath.Join(root.name, filepath.FromSlash(source.Path)), CommonDirectoryPath: filepath.Join(root.name, filepath.FromSlash(repo.commonPath)), Directory: directory, CommonDirectory: common})
	}
	if !s.rootUnchanged(root) || ctx.Err() != nil {
		return nil, errStale
	}
	success = true
	return pins, nil
}
