//go:build linux

package runner

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClusterWorkspaceStoragePlanEnforcement(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		standard    bool
	}{
		{name: "absent"},
		{name: "bounded", field: `,"unbounded_storage":false`},
		{name: "standard", field: `,"unbounded_storage":true`, standard: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p clusterworkspace.Plan
			if err := json.Unmarshal([]byte(`{"session_id":"session"`+tc.field+`}`), &p); err != nil {
				t.Fatal(err)
			}
			p.Manifest = protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/one"}}}
			p.Runtime = protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}
			p.Members = []clusterworkspace.Member{{ID: "one", Provider: "github", CloneURL: "https://github.com/org/one.git", PushURL: "https://github.com/org/one.git", CredentialHost: "github.com", DestinationIdentity: "org/one"}}
			root := t.TempDir()
			disk, rt, err := prepareClusterDisk(Config{Home: filepath.Join(root, "home"), ClusterPrivateRoot: filepath.Join(root, "private"), ClusterWriters: requestWriters{}, SessionID: p.SessionID, Workspace: &p})
			if tc.standard {
				if err != nil || disk == nil || rt == nil {
					t.Fatalf("standard plan did not bypass physical capacity: %v", err)
				}
				disk.Close()
			} else {
				if disk != nil {
					disk.Close()
				}
				if err == nil {
					t.Fatalf("missing/false flag must enforce bounded capacity: %v", err)
				}
			}
		})
	}
}
