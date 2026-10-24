package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkspaceRuntimeBinding(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		m := testManifest()
		m.Runtime = runtime
		m.Root = WorkspaceRoot{}
		if err := m.Validate(); err != nil {
			t.Fatal("selection precedes allocation", err)
		}
		b := WorkspaceRuntimeBinding{WorkspaceRuntimeRef: WorkspaceRuntimeRef{m.LaunchGeneration, 1}, ManifestDigest: m.Digest, Root: WorkspaceRoot{Device: 3, Inode: 4}}
		if err := b.Validate(m); err != nil {
			t.Fatal(err)
		}
		in := WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: "session", Binding: b}
		raw, _ := json.Marshal(in)
		var out WorkspaceRuntimePrepared
		if err := json.Unmarshal(raw, &out); err != nil || !reflect.DeepEqual(in, out) {
			t.Fatal("round trip", err)
		}
		for _, mutate := range []func(*WorkspaceRuntimeBinding){func(b *WorkspaceRuntimeBinding) { b.Generation = 0 }, func(b *WorkspaceRuntimeBinding) { b.Generation = -1 }, func(b *WorkspaceRuntimeBinding) { b.LaunchGeneration = "old" }, func(b *WorkspaceRuntimeBinding) { b.ManifestDigest = "other" }, func(b *WorkspaceRuntimeBinding) { b.Root.Inode = 0 }} {
			bad := b
			mutate(&bad)
			if bad.Validate(m) == nil {
				t.Fatal("invalid binding", bad)
			}
		}
		if b.Matches(nil) {
			t.Fatal("pending root authorized")
		}
		next := b
		next.Generation++
		if b.Matches(&next) {
			t.Fatal("stale request accepted")
		}
		if !b.Matches(&b) {
			t.Fatal("current refused")
		}
	}
}
