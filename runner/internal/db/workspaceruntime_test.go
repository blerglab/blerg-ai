package db_test

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync"
	"testing"
)

func TestWorkspaceRuntimeReplacement(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			pool := connect(t)
			ctx := context.Background()
			const daemon = "00000000-0000-0000-0000-000000000001"
			const id = "00000000-0000-0000-0000-000000000002"
			if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertSession(ctx, pool, id, daemon, "running", "/repos/one", "org/one", "", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id='owner',runtime=$2 WHERE id=$1`, id, runtime); err != nil {
				t.Fatal(err)
			}
			m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: runtime, LaunchGeneration: "selection", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "org/one"}}}
			if err := db.InsertSessionWorkspace(ctx, pool, id, m); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BeginWorkspaceRuntime(ctx, pool, id, "wrong", 0); err == nil {
				t.Fatal("wrong selection")
			}
			gen, err := db.BeginWorkspaceRuntime(ctx, pool, id, m.LaunchGeneration, 0)
			if err != nil || gen != 1 {
				t.Fatal(gen, err)
			}
			_, pending, err := db.GetWorkspaceRuntime(ctx, pool, id)
			if err != nil || pending != nil {
				t.Fatal("pending", err)
			}
			b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: gen}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}
			results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: m.LaunchGeneration}, Head: "successful-head"}}
			if db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, results) == nil {
				t.Fatal("unprepared writer")
			}
			if err := db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b); err != nil {
				t.Fatal(err)
			}
			if err := db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b); err != nil {
				t.Fatal("exact retry", err)
			}
			for _, mutate := range []func(*protocol.WorkspaceRuntimeBinding){func(b *protocol.WorkspaceRuntimeBinding) { b.Root.Inode++ }, func(b *protocol.WorkspaceRuntimeBinding) { b.ManifestDigest = "forged" }, func(b *protocol.WorkspaceRuntimeBinding) { b.Generation++ }} {
				bad := b
				mutate(&bad)
				if db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, bad) == nil {
					t.Fatal("binding replaced/forged")
				}
			}
			if err := db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, results); err != nil {
				t.Fatal(err)
			}
			if db.SetWorkspaceResults(ctx, pool, id, m.LaunchGeneration, nil) == nil {
				t.Fatal("legacy bypass")
			}
			var wg sync.WaitGroup
			wins := make(chan int64, 2)
			for range 2 {
				wg.Go(func() {
					if n, e := db.BeginWorkspaceRuntime(ctx, pool, id, m.LaunchGeneration, gen); e == nil {
						wins <- n
					}
				})
			}
			wg.Wait()
			close(wins)
			var generations []int64
			for n := range wins {
				generations = append(generations, n)
			}
			if len(generations) != 1 || generations[0] != 2 {
				t.Fatal("CAS", generations)
			}
			if db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b) == nil {
				t.Fatal("stale binding")
			}
			if db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, nil) == nil {
				t.Fatal("stale erased results")
			}
			w, err := db.GetSessionWorkspace(ctx, pool, id)
			if err != nil || len(w.Results) != 1 || w.Results[0].Head != "successful-head" || w.Manifest.Root.Inode != 0 {
				t.Fatal("selection/results lost", err)
			}
			b.Generation = 2
			b.Root.Inode = 3
			if err := db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b); err != nil {
				t.Fatal(err)
			}
			oldRef := b.WorkspaceRuntimeRef
			oldRef.Generation--
			if db.SetWorkspaceRuntimeResults(ctx, pool, id, oldRef, nil) == nil {
				t.Fatal("old writer accepted after new preparation")
			}
			n, current, err := db.GetWorkspaceRuntime(ctx, pool, id)
			if err != nil || n != 2 || current == nil || *current != b {
				t.Fatal("current", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE sessions SET status='stopped',ended_at=now() WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BeginWorkspaceRuntime(ctx, pool, id, m.LaunchGeneration, 2); err == nil {
				t.Fatal("ended replacement")
			}
			if db.BindWorkspaceRuntime(ctx, pool, id, b.WorkspaceRuntimeRef, b) == nil {
				t.Fatal("ended binding")
			}
			if db.SetWorkspaceRuntimeResults(ctx, pool, id, b.WorkspaceRuntimeRef, nil) == nil {
				t.Fatal("ended report")
			}
		})
	}
}
