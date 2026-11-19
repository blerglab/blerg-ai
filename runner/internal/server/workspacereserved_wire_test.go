//go:build linux

package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	raw, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return string(raw)
}

// A one-repository host workspace keeps its first repository at the root. The
// second sits in the reserved folder: Git never sees it, the reader shows it as
// a normal member, and it survives a restart and leaves nothing behind on Remove.
func TestMembershipHostOneRepositoryAddRealWire(t *testing.T) {
	s := newMembershipStack(t, "")
	oneRef, twoRef := s.repository("project", "one"), s.repository("elsewhere", "two")
	one, two := filepath.Join(s.repos, "project", "one"), filepath.Join(s.repos, "elsewhere", "two")
	sid := s.launch("daemon", oneRef)
	if view := s.view(sid); view.AddBlocked != "" {
		t.Fatalf("add is still blocked: %+v", view)
	}

	op := s.add(sid, 0, twoRef)
	s.waitPhase(op, db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || w.Manifest == nil || len(w.Manifest.Members) != 2 || w.Manifest.Members[0].Path != "." || w.Manifest.Members[1].Path != ".workspace/two" {
		t.Fatalf("manifest %+v %v", w, err)
	}
	if out := gitOutput(t, one, "status", "--porcelain", "--ignored=no"); out != "" {
		t.Fatalf("the first repository sees the reserved folder: %s", out)
	}
	if out := gitOutput(t, filepath.Join(one, ".workspace", "two"), "rev-parse", "--show-toplevel"); !strings.Contains(out, "two") {
		t.Fatalf("the path does not reach the new repository: %s", out)
	}

	listing := s.explorer(sid, "op=list")
	shown := false
	for _, entry := range listing.Entries {
		shown = shown || entry.Name == "two" && entry.Repository && entry.Path == ".workspace/two"
		if entry.Name == ".workspace" {
			t.Fatalf("the reserved folder is listed: %+v", listing)
		}
	}
	if !shown {
		t.Fatalf("explorer lacks the new member: %+v", listing)
	}
	if search := s.explorer(sid, "op=search&filter=README.md"); !strings.Contains(entriesText(search), ".workspace/two/README.md") || !strings.Contains(entriesText(search), "README.md") {
		t.Fatalf("search lacks a member: %+v", search)
	}
	if nested := s.explorer(sid, "op=list&path=.workspace/two"); !strings.Contains(entriesText(nested), "README.md") {
		t.Fatalf("the member cannot be listed: %+v", nested)
	}

	s.waitIdle(sid)
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 60*time.Second, "two member outcomes", func() bool {
		current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
		return e == nil && len(current.Results) == 2
	})

	s.waitIdle(sid)
	s.stop()
	s.start()
	p6EnabledWait(t, 60*time.Second, "resumed revision", func() bool {
		_, b, e := db.GetWorkspaceRuntime(s.ctx, s.f.pool, sid)
		row, rowErr := db.GetSession(s.ctx, s.f.pool, sid)
		return e == nil && b != nil && b.ManifestDigest == w.Manifest.Digest && rowErr == nil && row.Status == "idle"
	})
	if listing := s.explorer(sid, "op=list"); !strings.Contains(entriesText(listing), ".workspace/two") {
		t.Fatalf("restart lost the member: %+v", listing)
	}

	removed := s.remove(sid, 1, w.Manifest.Members[1].ID)
	s.waitPhase(removed, db.MembershipInstalled)
	if _, err := os.Lstat(filepath.Join(one, ".workspace")); !os.IsNotExist(err) {
		t.Fatalf("the reserved folder stayed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(one, ".git", "info", "exclude"))
	if err != nil && !os.IsNotExist(err) || strings.Contains(string(raw), ".workspace") {
		t.Fatalf("exclude kept the entry: %q %v", raw, err)
	}
	if out := gitOutput(t, two, "log", "--oneline", "-1"); out == "" {
		t.Fatal("removal touched the repository")
	}

	// It can come back.
	again := s.add(sid, 2, twoRef)
	s.waitPhase(again, db.MembershipInstalled)
	if out := s.request("GET", "/api/sessions/"+sid+"/workspace/repositories", nil, http.StatusOK); strings.Contains(string(out), "add_blocked") {
		t.Fatalf("view: %s", out)
	}
}

// In a sandbox the same layout is a real clone in the allocation. It reaches the
// agent at the path it was told, turn-end sync delivers it to its own branch, and
// the first repository's history never holds it, even as the session ends.
func TestMembershipSandboxOneRepositoryAddRealWire(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	s := newMembershipStack(t, p6SandboxImage(t, "blerg-runner-sandbox:latest"))
	oneRef, twoRef := s.repository("project", "one"), s.repository("elsewhere", "two")
	one, two := filepath.Join(s.repos, "project", "one"), filepath.Join(s.repos, "elsewhere", "two")
	sid := s.launch("docker", oneRef)
	defer func() {
		call, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		defer stop()
		_ = s.f.api.stopSessionWithDelivery(call, sid, systemEnd(db.EndReasonStoppedByUser), false)
	}()

	s.waitPhase(s.add(sid, 0, twoRef), db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(w.Manifest.Members) != 2 || w.Manifest.Members[1].Path != ".workspace/two" {
		t.Fatalf("manifest %+v %v", w, err)
	}
	container := strings.TrimSpace(string(mustOutput(t, "docker", "ps", "-q", "--filter", "label=blerg.runner.workspace-session="+sid)))
	if container == "" {
		t.Fatal("no labeled container to reach")
	}
	if out := string(mustOutput(t, "docker", "exec", "-w", "/workspace", container, "git", "status", "--porcelain")); strings.TrimSpace(out) != "" {
		t.Fatalf("the first repository sees the reserved folder: %s", out)
	}
	mustOutput(t, "docker", "exec", container, "test", "-f", "/workspace/.workspace/two/README.md")

	listing := s.explorer(sid, "op=list")
	if !strings.Contains(entriesText(listing), ".workspace/two") || strings.Contains(entriesText(listing), ".workspace ") {
		t.Fatalf("listing %+v", listing)
	}
	if search := s.explorer(sid, "op=search&filter=README.md"); !strings.Contains(entriesText(search), ".workspace/two/README.md") {
		t.Fatalf("search lacks the member: %+v", search)
	}

	mustOutput(t, "docker", "exec", "-w", "/workspace/.workspace/two", container, "sh", "-c", "echo work > added.txt && git add added.txt && git -c user.name=Agent -c user.email=agent@example.com commit -qm work")
	s.waitIdle(sid)
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 90*time.Second, "member delivery", func() bool {
		branch := strings.TrimSpace(gitOutput(t, two, "for-each-ref", "--format=%(refname)", "refs/heads/wip/"))
		return branch != "" && strings.Contains(gitOutput(t, two, "ls-tree", "-r", "--name-only", branch), "added.txt")
	})
	s.waitIdle(sid)

	// A restart ends the session and settles every member; the first repository's
	// branch holds its own files and nothing of the other member.
	s.stop()
	s.start()
	p6EnabledWait(t, 60*time.Second, "sandbox recovery", func() bool {
		row, e := db.GetSession(s.ctx, s.f.pool, sid)
		return e == nil && row != nil && row.EndedAt != nil
	})
	final, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil || len(final.Results) != 2 {
		t.Fatalf("final results %+v %v", final, err)
	}
	for _, ref := range strings.Fields(gitOutput(t, one, "for-each-ref", "--format=%(refname)", "refs/heads/")) {
		if tree := gitOutput(t, one, "ls-tree", "-r", "--name-only", ref); strings.Contains(tree, ".workspace") {
			t.Fatalf("%s absorbed the reserved folder: %s", ref, tree)
		}
	}
}

// The finding's path through the shipped daemon: delivered, then an ignored file
// from a shell. Remove refuses and names it, nothing is lost, Main can't confirm,
// and the person's confirmation deletes only what they saw.
func TestMembershipSandboxRemoveIgnoredFilesRealWire(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("build the production daemon and supply BLERG_HOST_HELPER_TEST_BINARY")
	}
	s := newMembershipStack(t, p6SandboxImage(t, "blerg-runner-sandbox:latest"))
	oneRef, twoRef := s.repository("project", "one"), s.repository("elsewhere", "two")
	sid := s.launch("docker", oneRef)
	defer func() {
		call, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		defer stop()
		_ = s.f.api.stopSessionWithDelivery(call, sid, systemEnd(db.EndReasonStoppedByUser), false)
	}()
	s.waitPhase(s.add(sid, 0, twoRef), db.MembershipInstalled)
	w, err := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	two := w.Manifest.Members[1]
	s.waitIdle(sid)
	s.request("POST", "/api/runner/sessions/"+sid+"/message", map[string]string{"text": "commit both members", "client_id": newUUID()}, 202)
	p6EnabledWait(t, 90*time.Second, "delivered results", func() bool {
		current, e := db.GetSessionWorkspace(s.ctx, s.f.pool, sid)
		if e != nil || len(current.Results) != 2 {
			return false
		}
		for _, result := range current.Results {
			if result.Pending || result.SyncError != "" {
				return false
			}
		}
		return true
	})
	s.waitIdle(sid)
	container := strings.TrimSpace(string(mustOutput(t, "docker", "ps", "-q", "--filter", "label=blerg.runner.workspace-session="+sid)))
	clone := "/workspace/" + two.Path
	mustOutput(t, "docker", "exec", container, "sh", "-c", "cd "+clone+" && echo 'cache/' > .git/info/exclude && mkdir cache && echo local > cache/blob")

	refused := s.waitPhase(s.remove(sid, 1, two.ID), db.MembershipRefused)
	if strings.Join(refused.LocalFiles.Names, " ") != "cache/" || refused.LocalFiles.Digest == "" {
		t.Fatalf("refusal %+v", refused)
	}
	mustOutput(t, "docker", "exec", container, "test", "-f", clone+"/cache/blob")
	view := s.view(sid)
	if view.Last == nil || len(view.Last.LocalFiles) != 1 || view.Last.LocalDigest != refused.LocalFiles.Digest || !strings.Contains(view.Last.Refusal, "never delivered") {
		t.Fatalf("view %+v", view.Last)
	}

	change := func(digest string) map[string]any {
		return map[string]any{"operation_id": newUUID(), "kind": "remove", "expected_revision": 1, "member_id": two.ID, "discard_local": digest}
	}
	path := "/api/sessions/" + sid + "/workspace/repositories"
	s.sessionRequest(sid, "POST", path, change(refused.LocalFiles.Digest), http.StatusForbidden)
	s.request("POST", path, change("not-the-digest"), http.StatusConflict)

	s.waitIdle(sid)
	body := change(refused.LocalFiles.Digest)
	s.request("POST", path, body, http.StatusAccepted)
	s.waitPhase(body["operation_id"].(string), db.MembershipInstalled)
	if out, err := exec.Command("docker", "exec", container, "test", "-e", clone).CombinedOutput(); err == nil {
		t.Fatalf("the confirmed clone stayed: %s", out)
	}
}
