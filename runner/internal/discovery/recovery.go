package discovery

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
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
	if root.name != saved.RootName || root.tag != saved.RootIdentity {
		return errStale
	}
	op := &operation{ctx: ctx}
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
