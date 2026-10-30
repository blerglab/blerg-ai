package discovery

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"path/filepath"
	"time"
)

// privateSelectionState is P2 recovery input, never a public/agent file. P2
// persists it with its integrity-protected private launch state and restores
// it only after authenticating the current owner and immutable launch digest.
type privateSelectionState struct {
	Manifest       protocol.WorkspaceManifest `json:"manifest"`
	RootName       string                     `json:"root_name"`
	RootIdentity   string                     `json:"root_identity"`
	Refs           []protocol.SelectionRef    `json:"refs"`
	Anchor         string                     `json:"anchor"`
	AnchorInstance string                     `json:"anchor_instance"`
	PhysicalFolder bool                       `json:"physical_folder"`
}

func (s *Service) SnapshotBound(m protocol.WorkspaceManifest) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.bound[m.LaunchGeneration]
	if !ok || m.Digest != workspaceselection.Digest(m) || m.Digest != record.manifest.Digest {
		return nil, errStale
	}
	return json.Marshal(privateSelectionState{Manifest: record.manifest, RootName: record.rootName, RootIdentity: record.resolved.RootIdentity, Refs: record.refs, Anchor: record.anchorPath, AnchorInstance: record.anchorInstance, PhysicalFolder: record.physicalFolder})
}
func (s *Service) RestoreBound(ctx context.Context, m protocol.WorkspaceManifest, raw []byte) error {
	if len(raw) > maxReply || m.Validate() != nil || m.Digest != workspaceselection.Digest(m) {
		return errInvalid
	}
	var saved privateSelectionState
	if json.Unmarshal(raw, &saved) != nil || saved.Manifest.Digest != m.Digest || workspaceselection.Digest(saved.Manifest) != m.Digest || len(saved.Refs) != len(m.Members) {
		return errInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.file.Close() }()
	return s.restore(root, &operation{ctx: ctx}, m, saved)
}

// RebindResumed restores the bound selection of a launch the daemon resumed
// after a restart, so its host children can be bound again. The input is the
// launch's own MAC-protected journal (anchor and member directories, absolute)
// and the server-authenticated manifest: every member is resolved again under
// the configured root and must have the manifest's identities, as for
// RestoreBound. Nothing comes from the agent or its workspace.
func (s *Service) RebindResumed(ctx context.Context, m protocol.WorkspaceManifest, anchor string, sources []string) error {
	if m.Validate() != nil || m.Digest != workspaceselection.Digest(m) || len(sources) != len(m.Members) || !filepath.IsAbs(anchor) {
		return errInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.file.Close() }()
	relative := func(name string) (string, error) {
		rel, err := filepath.Rel(root.name, filepath.Clean(name))
		if err != nil || !filepath.IsAbs(name) {
			return "", errInvalid
		}
		return cleanPath(filepath.ToSlash(rel))
	}
	saved := privateSelectionState{Manifest: m, RootName: root.name, RootIdentity: root.tag, Refs: []protocol.SelectionRef{}, PhysicalFolder: m.Kind == "folder"}
	if saved.Anchor, err = relative(anchor); err != nil {
		return err
	}
	for i, source := range sources {
		rel, err := relative(source)
		if err != nil {
			return err
		}
		saved.Refs = append(saved.Refs, protocol.SelectionRef{Path: rel, SourceIdentity: m.Members[i].SourceIdentity})
		// A multi-repository selection without a chosen folder has a synthetic
		// anchor (its first member); a physical folder contains every member.
		if filepath.Clean(source) != filepath.Join(filepath.Clean(anchor), filepath.FromSlash(m.Members[i].Path)) {
			saved.PhysicalFolder = false
		}
	}
	op := &operation{ctx: ctx}
	dir, _, err := safeDirectory(root, saved.Anchor, op)
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	if err == nil {
		saved.AnchorInstance = instanceIdentity(dir, info)
	}
	_ = dir.Close()
	if err != nil {
		return errStale
	}
	return s.restore(root, op, m, saved)
}

func (s *Service) restore(root *rootHandle, op *operation, m protocol.WorkspaceManifest, saved privateSelectionState) error {
	if root.name != saved.RootName || root.tag != saved.RootIdentity || (len(m.Members) == 0 && m.Root.Identity != root.tag) {
		return errStale
	}
	record := selectionRecord{rootName: root.name, runtime: m.Runtime, refs: saved.Refs, anchorPath: saved.Anchor, anchorInstance: saved.AnchorInstance, physicalFolder: saved.PhysicalFolder}
	if len(saved.Refs) > 0 {
		current, err := s.resolve(root, protocol.DiscoverWorkspace{Operation: "resolve", Layout: m.Layout, Members: saved.Refs, RootIdentity: root.tag, Runtime: m.Runtime}, op)
		if err != nil {
			return err
		}
		for i, source := range current.resolved.Members {
			member := m.Members[i]
			if member.ID != source.ID || member.SourceIdentity != source.SourceIdentity || member.CommonDirectoryIdentity != source.CommonDirectoryIdentity || member.Name != source.Name {
				return errStale
			}
		}
		record.resolved = current.resolved
		if !record.physicalFolder && (record.anchorPath != current.anchorPath || record.anchorInstance != current.anchorInstance) {
			return errStale
		}
	} else {
		record.resolved = protocol.DiscoveryResolved{Layout: m.Layout, RootIdentity: root.tag, SelectionIdentity: m.Digest, Root: m.Root, Members: []protocol.DiscoverySource{}}
	}
	if !recordAnchorCurrent(root, record, op) || !s.rootUnchanged(root) {
		return errStale
	}
	if record.physicalFolder {
		anchor, _, err := safeDirectory(root, record.anchorPath, op)
		if err != nil {
			return err
		}
		info, err := anchor.Stat()
		_ = anchor.Close()
		if err != nil {
			return errStale
		}
		dev, ino := fileIdentity(info)
		if dev != m.Root.Device || ino != m.Root.Inode {
			return errStale
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.bound[m.LaunchGeneration]; ok && old.manifest.Digest != m.Digest {
		return errStale
	}
	if _, ok := s.bound[m.LaunchGeneration]; !ok {
		if len(s.bound) >= maxRemembered {
			return errBudget
		}
		s.boundOrder = append(s.boundOrder, m.LaunchGeneration)
	}
	s.bound[m.LaunchGeneration] = boundRecord{selectionRecord: record, manifest: m}
	return nil
}
