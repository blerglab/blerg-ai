//go:build !linux

package clusterworkspace

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Cluster traversal requires Linux openat2; no weaker fallback.
type DiskGit struct{}

func NewDiskGit(string, string, string, ...MemberIdentity) (*DiskGit, error) { return nil, ErrPlan }
func NewDiskGitStorage(string, string, string, bool, ...MemberIdentity) (*DiskGit, error) {
	return nil, ErrPlan
}
func (d *DiskGit) Binding(ref protocol.WorkspaceRuntimeRef, digest string) protocol.WorkspaceRuntimeBinding {
	return protocol.WorkspaceRuntimeBinding{}
}
func (d *DiskGit) Close() {}
func (d *DiskGit) Prepare(context.Context, Member, string, bool) (string, string, error) {
	return "", "", ErrPlan
}
func (d *DiskGit) Head(context.Context, Member) (string, error)       { return "", ErrPlan }
func (d *DiskGit) Push(context.Context, Member, string, string) error { return ErrPlan }

func (d *DiskGit) Identities() []MemberIdentity { return nil }
