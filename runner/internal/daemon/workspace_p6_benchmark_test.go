package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Opt-in release measurements of the actual host explorer and quota-backed
// Docker clone/copy/settlement primitives. These deliberately make no claim of
// end-to-end engine, authenticated adapter or pod acceptance.
func TestP6WorkspaceBenchmarks(t *testing.T) {
	if os.Getenv("BLERG_P6_BENCHMARK") != "1" {
		t.Skip("set BLERG_P6_BENCHMARK=1 with the sandbox image")
	}
	previousContext := currentDockerContext
	currentDockerContext = realDockerContext
	t.Cleanup(func() { currentDockerContext = previousContext })
	for _, heavy := range []bool{false, true} {
		for _, count := range []int{1, 5, 20} {
			t.Run(fmt.Sprintf("heavy=%t/members=%d", heavy, count), func(t *testing.T) {
				t.Setenv(DaemonStateDirEnv, t.TempDir())
				root := t.TempDir()
				launch := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "benchmark-owner", Runtime: "sandbox", LaunchGeneration: "benchmark-launch", Digest: "benchmark-digest"}
				var sources []WorkspaceSource
				for n := 0; n < count; n++ {
					id := fmt.Sprintf("member-%02d", n)
					repo := filepath.Join(root, id)
					initChildRepo(t, repo)
					for i := 0; i < 32; i++ {
						if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%03d.txt", i)), []byte(strings.Repeat("fixture\n", 128)), 0600); err != nil {
							t.Fatal(err)
						}
					}
					childRepoGit(t, repo, "add", ".")
					childRepoGit(t, repo, "commit", "-m", "benchmark fixture")
					if heavy {
						head := childRepoGit(t, repo, "rev-parse", "HEAD")
						refs := filepath.Join(repo, ".git", "refs", "heads", "metadata")
						if err := os.MkdirAll(refs, 0700); err != nil {
							t.Fatal(err)
						}
						for i := 0; i < 1000; i++ {
							if err := os.WriteFile(filepath.Join(refs, fmt.Sprintf("ref-%04d", i)), []byte(head+"\n"), 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
					member := protocol.WorkspaceMember{ID: id, Name: id, Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "common-" + id, Destination: "destination-" + id}
					identity, err := workspaceDirectoryIdentity(repo)
					if err != nil {
						t.Fatal(err)
					}
					common, err := workspaceDirectoryIdentity(filepath.Join(repo, ".git"))
					if err != nil {
						t.Fatal(err)
					}
					launch.Members = append(launch.Members, member)
					sources = append(sources, WorkspaceSource{Member: member, Path: repo, RootIdentity: identity, CommonIdentity: common})
				}
				hostManifest := launch
				hostManifest.Runtime = "host"
				info, err := os.Stat(root)
				if err != nil {
					t.Fatal(err)
				}
				hostBinding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: launch.LaunchGeneration, Generation: 1}, ManifestDigest: launch.Digest, Root: workspaceRootIdentity(info)}
				start := time.Now()
				explorer, err := NewLocalExplorer(root, hostManifest, hostBinding, func() bool { return true })
				if err != nil {
					t.Fatal(err)
				}
				result := explorer.Explore(t.Context(), protocol.WorkspaceExplorerRequest{Operation: "search", Path: "", Filter: "file-", WorkspaceRuntimeRef: hostBinding.WorkspaceRuntimeRef})
				if !result.OK {
					t.Fatal("host search refused")
				}
				hostSearch := time.Since(start)
				_ = explorer.Close()
				storage := defaultWorkspaceStorage()
				session := "p6-benchmark-" + ccUUID()
				start = time.Now()
				w, err := PrepareLocalWorkspace(t.Context(), session, launch, sources, storage)
				if err != nil {
					t.Fatal(err)
				}
				prepare := time.Since(start)
				label := "p6-benchmark-" + ccUUID()
				plan, err := w.DockerMountPlan(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				policy, err := workspaceEntrySecurityOption()
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"run", "--security-opt", policy, "--pull=never", "-d", "--network=none", "--init", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "blerg-test-run=" + label}
				for _, l := range plan.Labels {
					args = append(args, "--label", l)
				}
				for _, m := range plan.Mounts {
					args = append(args, "--mount", m)
				}
				args = append(args, "--entrypoint", "sleep", sandboxImage(), "600")
				id := p2Docker(t, args...)
				t.Cleanup(func() { cleanupP2Containers(t, label) })
				d := &DockerWorkspaceContainer{Context: realDockerContext(), ID: id, Owner: sandboxOwner(), Session: session, Launch: launch.LaunchGeneration, Paths: map[string]bool{}, CancelStatus: func(context.Context) error { return nil }}
				for _, m := range launch.Members {
					d.Paths[m.Path] = true
				}
				if _, err = w.BindRuntime(t.Context(), d); err != nil {
					t.Fatal(err)
				}
				for _, m := range launch.Members {
					p2Docker(t, "exec", "--workdir", filepath.Join(sandboxWorkdir, m.Path), id, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-m", "benchmark turn")
				}
				start = time.Now()
				_, err = w.Sync(t.Context())
				syncTime := time.Since(start)
				if err != nil {
					t.Fatal(err)
				}
				start = time.Now()
				_, err = w.Stop(t.Context())
				stop := time.Since(start)
				if err != nil {
					t.Fatal(err)
				}
				if !w.state.StorageReleased {
					t.Fatal("benchmark allocation retained")
				}
				t.Logf("P6_BENCHMARK heavy=%t members=%d host_search_ms=%.3f prepare_ms=%.3f sync_ms=%.3f stop_ms=%.3f hits=%d truncated=%t", heavy, count, float64(hostSearch.Microseconds())/1000, float64(prepare.Microseconds())/1000, float64(syncTime.Microseconds())/1000, float64(stop.Microseconds())/1000, len(result.Entries), result.Truncated)
			})
		}
	}
}
