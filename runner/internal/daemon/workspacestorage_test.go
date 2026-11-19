package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceCacheOnlySettlement(t *testing.T) {
	for _, cache := range []string{"__pycache__", ".pytest_cache", "node_modules/.cache", ".mypy_cache", ".ruff_cache"} {
		t.Run(cache, func(t *testing.T) {
			w, _, storage, _ := localWorkspaceFixture(t)
			m := &w.state.Members[0]
			if err := os.WriteFile(filepath.Join(m.Clone, ".gitignore"), []byte(cache+"/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			contents := map[string]string{"__pycache__": "module.cpython-312.pyc", ".pytest_cache": "v/cache/nodeids", "node_modules/.cache": "webpack/default-development/0.pack", ".mypy_cache": "3.12/module.meta.json", ".ruff_cache": "0.9.6/123456"}
			file := filepath.Join(m.Clone, cache, contents[cache])
			dir := filepath.Dir(file)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("generated"), 0600); err != nil {
				t.Fatal(err)
			}
			clean, err := settleWorkspaceMember(t.Context(), m, w.state.Session, w.branch())
			if err != nil || !clean {
				t.Fatalf("cache-only copy retained: clean=%v err=%v", clean, err)
			}
			if _, err := w.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !storage.released || !w.state.StorageReleased {
				t.Fatal("cache-only allocation retained")
			}

		})
	}
}

func TestWorkspaceCachePolicyIsNarrow(t *testing.T) {
	for _, name := range []string{"src/__pycache__/x.pyc", ".pytest_cache/v/cache/nodeids", "node_modules/.cache/webpack/default-development/index.pack", "nested/.mypy_cache/3.12/pkg/mod.data.json", ".ruff_cache/0.9.6/123456"} {
		if !workspaceRegenerableCache(name) {
			t.Errorf("cache refused: %s", name)
		}
	}
	for _, name := range []string{"__pycache__", "node_modules/package/index.js", ".cache/local", "my__pycache__/work", "node_modules/.cache", ".pytest_cache_notes/work"} {
		if workspaceRegenerableCache(name) {
			t.Errorf("user file treated as cache: %s", name)
		}
	}
}

func TestWorkspaceStoragePreviewListsUntrackedAndBindsBytes(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	clone := w.state.Members[0].Clone
	for name, contents := range map[string]string{"untracked.txt": "keep", ".gitignore": "private/\n", "private/token": "private"} {
		path := filepath.Join(clone, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.state.Root, "outside.txt"), []byte("outside repository"), 0600); err != nil {
		t.Fatal(err)
	}
	w.state.Stopped = true
	p, err := workspaceStorageAudit(t.Context(), w.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one/untracked.txt", "one/private/", "outside.txt"} {
		if !slices.Contains(p.Files, name) {
			t.Errorf("preview omitted %s: %+v", name, p)
		}
	}
	if err := os.WriteFile(filepath.Join(clone, "untracked.txt"), []byte("edit"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := workspaceStorageAudit(t.Context(), w.state)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest == next.Digest {
		t.Fatal("confirmation did not bind bytes")
	}
}

func TestWorkspaceStoragePrunesOnlyAcknowledgedRelease(t *testing.T) {
	w, _, storage, _ := localWorkspaceFixture(t)
	if err := pruneWorkspaceStorage(w, storage); err == nil {
		t.Fatal("pruned before acknowledgment")
	}
	w.state.StorageReleased = true
	w.state.Binding = &protocol.WorkspaceRuntimeBinding{}
	w.state.FinalDelivery = &protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	w.state.FinalResults = w.results()
	transaction := WorkspaceFinalization{Binding: *w.state.Binding, Message: *w.state.FinalDelivery, Results: w.state.FinalResults}
	w.state.TerminalReceipt = transaction.Digest()
	w.state.TerminalAcknowledged = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := pruneWorkspaceStorage(w, storage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.record); !os.IsNotExist(err) {
		t.Fatal("workspace record retained", err)
	}
}

func TestWorkspaceStorageConfirmationAndPruning(t *testing.T) {
	w, runtime, storage, _ := localWorkspaceFixture(t)
	clone := w.state.Members[0].Clone
	if err := os.WriteFile(filepath.Join(clone, "local.txt"), []byte("unsaved"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.gone = true
	w.state.Stopped = true
	w.state.RuntimeIdentity = "test:" + strings.Repeat("a", 64)
	w.state.Binding = &protocol.WorkspaceRuntimeBinding{}
	w.state.FinalDelivery = &protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	w.state.FinalResults = w.results()
	transaction := WorkspaceFinalization{Binding: *w.state.Binding, Message: *w.state.FinalDelivery, Results: w.state.FinalResults}
	w.state.TerminalReceipt = transaction.Digest()
	w.state.TerminalAcknowledged = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	service := &LocalWorkspaceService{Storage: storage, sessions: map[string]*LocalWorkspace{w.state.Session: w}}
	for i := range 510 {
		if err := os.WriteFile(filepath.Join(w.state.Root, fmt.Sprintf("outside-%03d.txt", i)), []byte("user work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	req := protocol.WorkspaceStorageRequest{SessionID: w.state.Session, OwnerID: w.state.Launch.OwnerID, Operation: "preview"}
	p, err := service.storageOperation(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Files, "one/local.txt") {
		t.Fatal(p)
	}
	if !p.Truncated || p.FileCount < 510 {
		t.Fatal("large confirmation did not expose total count")
	}
	req.Operation = "release"
	if _, err := service.storageOperation(t.Context(), req); err == nil {
		t.Fatal("released without confirmation")
	}
	if storage.released {
		t.Fatal("storage released before confirmation")
	}
	req.Confirm = p.Digest
	if err := os.WriteFile(filepath.Join(clone, "local.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.storageOperation(t.Context(), req); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	req.Operation = "preview"
	p, err = service.storageOperation(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Operation = "release"
	req.Confirm = p.Digest
	released, err := service.storageOperation(t.Context(), req)
	if err != nil || !released.Released || !storage.released {
		t.Fatal(released, err)
	}
	if _, err := os.Stat(w.record); err != nil {
		t.Fatal("journal removed before release acknowledgment", err)
	}
	req.Operation = "acknowledge"
	if _, err := service.storageOperation(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.record); !os.IsNotExist(err) {
		t.Fatal("acknowledged journal retained", err)
	}
}

func TestWorkspaceStoragePrunesRemoteAllocationRecords(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	w.state.StorageReleased = true
	w.state.Binding = &protocol.WorkspaceRuntimeBinding{}
	w.state.FinalDelivery = &protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	w.state.FinalResults = w.results()
	transaction := WorkspaceFinalization{Binding: *w.state.Binding, Message: *w.state.FinalDelivery, Results: w.state.FinalResults}
	w.state.TerminalReceipt = transaction.Digest()
	w.state.TerminalAcknowledged = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	storage := &DockerWorkspaceStorage{Root: filepath.Join(t.TempDir(), "volumes")}
	r := workspaceRemoteRecord{Session: w.state.Session, Token: "allocation", Context: "test-context", Released: true}
	w.state.Root = r.logical()
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := storage.save(r); err != nil {
		t.Fatal(err)
	}
	path, err := storage.recordPath(w.state.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruneWorkspaceStorage(w, storage); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{w.record, filepath.Dir(path)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("per-session record retained", err)
		}
	}
}

func TestWorkspaceStorageAutomaticReleasePrunedAfterTerminalReceipt(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	if err := w.storage.Release(t.Context(), w.state.Session, w.state.Root); err != nil {
		t.Fatal(err)
	}
	w.state.StorageReleased = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	s.CommitFinal = acknowledgeWorkspaceFinal
	if err := s.Finalize(t.Context(), w, protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.record); !os.IsNotExist(err) {
		t.Fatal("acknowledged automatic release retained journal", err)
	}
	if err := s.Finalize(t.Context(), w, *w.state.FinalDelivery, nil); err != nil {
		t.Fatal("duplicate acknowledgment failed", err)
	}
}

func TestWorkspaceCachePolicyRetainsUserFiles(t *testing.T) {
	for _, name := range []string{"private/local.txt", "__pycache__"} {
		t.Run(name, func(t *testing.T) {
			w, _, storage, _ := localWorkspaceFixture(t)
			clone := w.state.Members[0].Clone
			if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("private/\n__pycache__\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(clone, name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(clone, name), []byte("user work"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if storage.released {
				t.Fatal("user work discarded as cache")
			}
		})
	}
}

func TestWorkspaceCacheReviewRetainsUnexpectedContents(t *testing.T) {
	for _, name := range []string{"tools/__pycache__/notes.md", "data/.mypy_cache/run.sqlite", "x/__pycache__/repo/.git/HEAD"} {
		t.Run(name, func(t *testing.T) {
			w, _, storage, _ := localWorkspaceFixture(t)
			clone := w.state.Members[0].Clone
			if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("__pycache__/\n.mypy_cache/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(clone, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("unsaved work"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if storage.released {
				t.Fatal("unexpected cache contents deleted automatically")
			}
		})
	}
}

func TestWorkspaceStorageLargePreviewCanBeConfirmed(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	for i := range 510 {
		if err := os.WriteFile(filepath.Join(w.state.Root, fmt.Sprintf("local-%03d.txt", i)), []byte("work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := workspaceStorageAudit(t.Context(), w.state)
	if err != nil {
		t.Fatal("large copy cannot be released", err)
	}
	if p.FileCount != 510 || !p.Truncated {
		t.Fatal("missing truncation count")
	}
	if len(p.Files) > 500 || len(p.Files) == 0 {
		t.Fatal("unbounded or empty listing")
	}
	// The digest must cover paths omitted from the displayed listing.
	if err := os.WriteFile(filepath.Join(w.state.Root, "local-509.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := workspaceStorageAudit(t.Context(), w.state)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest == next.Digest {
		t.Fatal("omitted file not bound to confirmation")
	}
}

func TestWorkspaceStoragePruneRejectsWrongJournalName(t *testing.T) {
	w, _ := finalWorkspaceFixture(t)
	w.state.StorageReleased = true
	w.state.FinalDelivery = &protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	w.state.FinalResults = w.results()
	transaction := WorkspaceFinalization{Binding: *w.state.Binding, Message: *w.state.FinalDelivery, Results: w.state.FinalResults}
	w.state.TerminalReceipt = transaction.Digest()
	w.state.TerminalAcknowledged = true
	w.record = filepath.Join(sandboxCloneRoot(), "other-session.workspace.json")
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := pruneWorkspaceStorage(w, w.storage); err == nil {
		t.Fatal("pruned mismatched journal name")
	}
	if _, err := os.Stat(w.record); err != nil {
		t.Fatal("mismatched journal removed", err)
	}
}

func TestWorkspaceStorageRecoveryRejectsWrongJournalName(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	if err := os.Rename(w.record, filepath.Join(sandboxCloneRoot(), "other-session.workspace.json")); err != nil {
		t.Fatal(err)
	}
	s.Authority = &testWorkspaceAuthority{}
	s.Supervisor = &testWorkspaceSupervisor{}
	s.CommitFinal = acknowledgeWorkspaceFinal
	called := false
	s.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) {
		called = true
		return w.state.Launch, nil
	}
	if err := s.RecoverAll(t.Context(), nil); err == nil {
		t.Fatal("mismatched recovery journal accepted")
	}
	if called {
		t.Fatal("mismatched record selected its embedded session authority")
	}
}

func TestWorkspaceStoragePreviewBoundsEncodedBytes(t *testing.T) {
	p := &protocol.WorkspaceStoragePreview{Digest: "digest"}
	for range 600 {
		p.Files = append(p.Files, strings.Repeat("\x00", 1024))
		p.Commits = append(p.Commits, strings.Repeat("reference", 1024))
	}
	boundWorkspaceStoragePreview(p)
	raw, err := json.Marshal(protocol.WorkspaceStorageResult{Preview: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 128<<10 || !p.Truncated || p.FileCount != 600 || p.CommitCount != 600 || p.Digest != "digest" {
		t.Fatalf("unbounded or incomplete preview: bytes=%d files=%d commits=%d", len(raw), p.FileCount, p.CommitCount)
	}
}

func TestWorkspaceCacheNestedGitAlwaysRefuses(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := &w.state.Members[0]
	if err := os.MkdirAll(filepath.Join(m.Clone, "tools/.pytest_cache/repo/.git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Clone, ".gitignore"), []byte(".pytest_cache/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	clean, err := settleWorkspaceMember(t.Context(), m, w.state.Session, w.branch())
	if clean || err != nil || m.Result.SyncNote != workspaceNestedRepositoryNotice {
		t.Fatal("empty nested Git directory did not refuse automatic release", clean, err)
	}
}

func TestWorkspaceCacheSymlinkRequiresConfirmation(t *testing.T) {
	w, _, storage, _ := localWorkspaceFixture(t)
	clone := w.state.Members[0].Clone
	if err := os.Mkdir(filepath.Join(clone, "__pycache__"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("__pycache__/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../source-one.txt", filepath.Join(clone, "__pycache__/module.pyc")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if storage.released {
		t.Fatal("cache symlink deleted automatically")
	}
}

func TestWorkspaceStoragePreviewReservesCommitBudget(t *testing.T) {
	p := &protocol.WorkspaceStoragePreview{Digest: "full-inventory", Commits: []string{"app: refs/heads/unsaved abc123", "app: refs/stash def456"}}
	for range 600 {
		p.Files = append(p.Files, strings.Repeat("f", 1021))
	}
	boundWorkspaceStoragePreview(p)
	if len(p.Commits) != 2 || p.CommitCount != 2 {
		t.Fatal("files crowded unsaved commits out of preview", p.Commits)
	}
	if !p.Truncated || len(p.Files) == 0 || p.FileCount != 600 || p.Digest != "full-inventory" {
		t.Fatal("file listing or digest lost")
	}
}

func TestWorkspaceNestedRepositoryRetainedWithoutSyncError(t *testing.T) {
	w, _, storage, _ := localWorkspaceFixture(t)
	if err := os.MkdirAll(filepath.Join(w.state.Members[0].Clone, "tools/.pytest_cache/repo/.git"), 0700); err != nil {
		t.Fatal(err)
	}
	results, err := w.Stop(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if storage.released || w.state.StorageReleased {
		t.Fatal("nested repository released automatically")
	}
	if results[0].SyncError != "" || results[0].Pending {
		t.Fatalf("retention reported as failed sync: %+v", results[0])
	}
	if results[0].SyncNote != "A nested repository is kept; release it from Release storage." {
		t.Fatal("missing retained-storage notice", results[0].SyncNote)
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Members[0].Result.SyncNote != results[0].SyncNote {
		t.Fatal("retention notice was not journaled")
	}
}
