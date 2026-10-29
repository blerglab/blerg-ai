package daemon

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceExplorerRuntimeHelper(t *testing.T) {
	root := t.TempDir()
	writeExplorerFile(t, root, "visible.txt")
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = writer.Close() }()
	reader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = output.Close() }()
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest"}
	// The manifest has no physical root. The helper must pin and report its own.
	done := make(chan error, 1)
	go func() { done <- ServeWorkspaceExplorer(t.Context(), root, input, output) }()
	encoder := json.NewEncoder(writer)
	decoder := json.NewDecoder(reader)
	start := WorkspaceExplorerStart{SessionID: "session", Manifest: manifest, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}}
	if err := encoder.Encode(start); err != nil {
		t.Fatal(err)
	}
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var prepared protocol.WorkspaceRuntimePrepared
	if err := decoder.Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Binding.Validate(manifest) != nil || prepared.Binding.Root.Inode == 0 {
		t.Fatal("helper failed to report its runtime root")
	}
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "request", SessionID: "session", WorkspaceRuntimeRef: start.Runtime, Operation: "list"}
	if err := encoder.Encode(req); err != nil {
		t.Fatal(err)
	}
	var result protocol.WorkspaceExplorerResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Entries) != 1 || result.Entries[0].Name != "visible.txt" {
		t.Fatalf("helper reply: %+v", result)
	}
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper cleanup failed")
	}
	bad := prepared.Binding
	bad.Root.Inode++
	if e, err := NewLocalExplorer(root, manifest, bad, func() bool { return true }); err == nil {
		_ = e.Close()
		t.Fatal("runtime binding ignored root identity")
	}
}
