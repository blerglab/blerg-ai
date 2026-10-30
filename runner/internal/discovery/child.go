package discovery

import (
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func (s *Service) bindChild(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) error {
	if req.Manifest == nil || req.Manifest.Digest != workspaceselection.Digest(*req.Manifest) {
		return errInvalid
	}
	s.mu.Lock()
	parent, ok := s.bound[req.ParentLaunchGeneration]
	s.mu.Unlock()
	m := *req.Manifest
	if !ok || protocol.ValidateWorkspaceChild(parent.manifest, m) != nil || m.Root != parent.manifest.Root || m.Kind != parent.manifest.Kind {
		return errInvalid
	}
	if parent.manifest.Runtime == "host" && parent.manifest.Kind == "folder" && len(parent.manifest.Members) == 0 {
		return errInvalid
	}
	refs := []protocol.SelectionRef{}
	for _, member := range m.Members {
		for i, p := range parent.manifest.Members {
			if p.ID == member.ID {
				refs = append(refs, parent.refs[i])
				break
			}
		}
	}
	record := selectionRecord{rootName: parent.rootName, runtime: parent.runtime, refs: refs, anchorPath: parent.anchorPath, anchorInstance: parent.anchorInstance, physicalFolder: parent.physicalFolder}
	if root.tag != parent.resolved.RootIdentity || root.name != parent.rootName {
		return errStale
	}
	if len(refs) > 0 {
		current, err := s.resolve(root, protocol.DiscoverWorkspace{Operation: "resolve", Layout: m.Layout, RootIdentity: root.tag, Members: refs, Runtime: parent.runtime}, op)
		if err != nil {
			return err
		}
		record.resolved = current.resolved
		if !parent.physicalFolder {
			record.anchorPath = current.anchorPath
			record.anchorInstance = current.anchorInstance
		}
	} else {
		record.resolved = protocol.DiscoveryResolved{Layout: m.Layout, RootIdentity: root.tag, SelectionIdentity: parent.resolved.SelectionIdentity, Root: parent.resolved.Root, Members: []protocol.DiscoverySource{}}
	}
	if !recordAnchorCurrent(root, record, op) {
		return errStale
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.bound[m.LaunchGeneration]; exists {
		if old.manifest.Digest != m.Digest {
			return errStale
		}
	} else {
		if len(s.bound) >= maxRemembered {
			return errBudget
		}
		s.boundOrder = append(s.boundOrder, m.LaunchGeneration)
	}

	s.bound[m.LaunchGeneration] = boundRecord{selectionRecord: record, manifest: m}

	return nil
}
func recordAnchorCurrent(root *rootHandle, record selectionRecord, op *operation) bool {
	f, _, err := safeDirectory(root, record.anchorPath, op)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	return err == nil && instanceIdentity(f, info) == record.anchorInstance
}
