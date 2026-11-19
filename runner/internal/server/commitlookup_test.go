package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const fixtureSHA = "0123456789abcdef0123456789abcdef01234567"

func TestCommitBrokerBindsConnectionSessionAndConsumesOnce(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	done := make(chan error, 1)
	go func() {
		_, err := h.requestCommit(t.Context(), dc, "acct", "session", "0123456", false, 30*time.Second)
		done <- err
	}()
	var req protocol.ReadCommit
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	if req.Type != "read_commit" || req.SHA != "0123456" || req.SessionID != "session" || req.Patch {
		t.Fatalf("request %+v", req)
	}
	reply := protocol.CommitResult{Type: "commit_result", RequestID: req.RequestID, SessionID: "session", OK: true}
	h.deliverCommitResult(&DaemonConn{ID: "other"}, reply) // another daemon cannot answer
	wrong := reply
	wrong.SessionID = "foreign"
	h.deliverCommitResult(dc, wrong) // nor for another session
	select {
	case err := <-done:
		t.Fatalf("forged reply accepted: %v", err)
	default:
	}
	h.deliverCommitResult(dc, reply)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.deliverCommitResult(dc, reply) // a consumed request cannot be answered twice
	h.commitReqs.mu.Lock()
	defer h.commitReqs.mu.Unlock()
	if len(h.commitReqs.pending) != 0 {
		t.Fatal("pending request leaked")
	}
}

type commitFx struct {
	*topicFx
	dc *DaemonConn
}

func newCommitFx(t *testing.T) *commitFx {
	fx := newTopicFx(t)
	fx.api.RegisterCommitRoutes(fx.mux)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true)
	dc.SetWorkBranches(true) // current daemon: immutable recorded branch names
	fx.api.hub.Register(dc)
	fixtureOwnDesktop(t, fx.pool, dc, topicOwner)
	for _, sid := range []string{fx.sess, fx.privSess, fx.ended} {
		fx.api.hub.SetSessionOwner(sid, dc.ID)
	}
	return &commitFx{fx, dc}
}

// refused asks and expects an answer with no request reaching the daemon.
func (fx *commitFx) refused(path, token string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	select {
	case raw := <-fx.dc.send:
		fx.t.Fatalf("a refused read reached the daemon: %s", raw)
	default:
	}
	return rec
}

// answered asks, and the daemon answers with reply(request).
func (fx *commitFx) answered(path, token string, reply func(protocol.ReadCommit) protocol.CommitResult) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); fx.mux.ServeHTTP(rec, req) }()
	var q protocol.ReadCommit
	select {
	case raw := <-fx.dc.send:
		if err := json.Unmarshal(raw, &q); err != nil {
			fx.t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		fx.t.Fatal("commit request not dispatched")
	}
	res := reply(q)
	res.Type, res.RequestID, res.SessionID = "commit_result", q.RequestID, q.SessionID
	fx.api.hub.deliverCommitResult(fx.dc, res)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		fx.t.Fatal("commit response did not complete")
	}
	return rec
}

func okCommit(q protocol.ReadCommit) protocol.CommitResult {
	return protocol.CommitResult{OK: true, Commit: &protocol.CommitInfo{
		SHA: fixtureSHA, Short: fixtureSHA[:7], Subject: "Fix the thing", Body: "Because.", Author: "Example Developer",
		AuthorDate: "2026-10-21T10:00:00Z", Parents: []string{strings.Repeat("a", 40)},
		Files: []protocol.CommitFile{{Path: "a.go", Adds: 3, Dels: 1}}, Adds: 3, Dels: 1,
	}}
}

// Security round 1, I1: the workspace-file reader's rule. The owner or an administrator, people
// only; someone else's private session is the uniform 404; everyone else 403, before the daemon.
func TestCommitLookupOwnerOrAdministratorOnly(t *testing.T) {
	fx := newCommitFx(t)
	url := func(sid string) string { return "/api/sessions/" + sid + "/commits/0123456" }
	owner := fx.token(topicOwner, "human")
	other := fx.token(topicOther, "human")
	admin := fx.token(topicOther, "human", "account.manage")

	rec := fx.answered(url(fx.sess), owner, okCommit)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["sha"] != fixtureSHA || got["subject"] != "Fix the thing" || got["request_id"] != nil || got["session_id"] != nil {
		t.Fatalf("reply %s", rec.Body)
	}
	// Administrator status never grants another person's desktop.
	if rec := fx.refused(url(fx.sess), admin); rec.Code != http.StatusForbidden {
		t.Fatalf("administrator: %d %s", rec.Code, rec.Body)
	}
	// A private session owner still needs the persisted desktop owner to match.
	if rec := fx.refused(url(fx.privSess), other); rec.Code != http.StatusForbidden {
		t.Fatalf("private session owner: %d %s", rec.Code, rec.Body)
	}
	// Someone who can read the transcript of a shared session cannot read its workspace's commits.
	if rec := fx.refused(url(fx.sess), other); rec.Code != http.StatusForbidden {
		t.Fatalf("a viewer of a shared session: %d %s", rec.Code, rec.Body)
	}
	msg := func(r *httptest.ResponseRecorder) string {
		var b struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &b)
		return b.Error
	}
	for _, tok := range []string{owner, fx.token(topicOwner, "human", "account.manage")} {
		priv := fx.refused(url(fx.privSess), tok)
		unknown := fx.refused(url(newUUID()), tok)
		if priv.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || msg(priv) != msg(unknown) {
			t.Fatalf("someone else's private session: %d %s; unknown: %d %s", priv.Code, priv.Body, unknown.Code, unknown.Body)
		}
	}
	// Tokens that are not a person: refused even for the owner's own session.
	for _, tok := range []string{fx.token(topicOwner, "agent"), fx.token(topicOwner, "service")} {
		if rec := fx.refused(url(fx.sess), tok); rec.Code != http.StatusForbidden {
			t.Fatalf("non-human token: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := fx.refused(url(fx.sess), fx.sessTok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session token: %d", rec.Code)
	}
	if rec := fx.refused(url(fx.sess), ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
}

// M9: one person's reads cannot take every server slot.
func TestCommitBrokerCapsReadsPerAccount(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var daemons []*DaemonConn
	for i := range 3 {
		dc := &DaemonConn{ID: "daemon-" + strconv.Itoa(i), send: make(chan []byte, 8)}
		h.Register(dc)
		daemons = append(daemons, dc)
	}
	done := make(chan error, commitPendingPerAccount)
	for i := range commitPendingPerAccount {
		sid := "session-" + strconv.Itoa(i)
		dc := daemons[i%2]
		h.SetSessionOwner(sid, dc.ID)
		go func() {
			_, err := h.requestCommit(ctx, dc, "greedy", sid, "0123456", false, 30*time.Second)
			done <- err
		}()
		<-dc.send
	}
	h.SetSessionOwner("session-x", daemons[2].ID)
	if _, err := h.requestCommit(ctx, daemons[2], "greedy", "session-x", "0123456", false, time.Second); !errors.Is(err, errCommitBusy) {
		t.Fatalf("over the per-account cap: %v", err)
	}
	// Someone else still gets a slot.
	go func() {
		_, _ = h.requestCommit(ctx, daemons[2], "patient", "session-x", "0123456", false, 30*time.Second)
	}()
	select {
	case <-daemons[2].send:
	case <-time.After(10 * time.Second):
		t.Fatal("another person's read was not sent")
	}
	cancel()
	for range commitPendingPerAccount {
		<-done
	}
}

func TestCommitLookupRefusesBeforeTheDaemon(t *testing.T) {
	fx := newCommitFx(t)
	owner := fx.token(topicOwner, "human")
	// An ended session has no workspace to read.
	if rec := fx.refused("/api/sessions/"+fx.ended+"/commits/0123456", owner); rec.Code != http.StatusConflict {
		t.Fatalf("ended session: %d %s", rec.Code, rec.Body)
	}
	for _, sha := range []string{"HEAD", "main", "012345", "ABCDEF0", "--output=x", "-p", "0123456..89abcde", "0123456%5E%7Btree%7D", strings.Repeat("a", 65), "0123456%0A"} {
		if rec := fx.refused("/api/sessions/"+fx.sess+"/commits/"+sha, owner); rec.Code != http.StatusNotFound {
			t.Fatalf("sha %q: %d %s", sha, rec.Code, rec.Body)
		}
	}
	fx.api.hub.SetSessionOwner(fx.sess, "missing-daemon")
	if rec := fx.refused("/api/sessions/"+fx.sess+"/commits/0123456", owner); rec.Code != http.StatusConflict {
		t.Fatalf("no daemon: %d", rec.Code)
	}
	fx.api.hub.SetSessionOwner(fx.sess, fx.dc.ID)
	// A daemon that never answers (an older one): the read gives up, nothing hangs.
	prev := commitLookupTimeout
	commitLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { commitLookupTimeout = prev })
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/commits/0123456", nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	<-fx.dc.send
	if rec.Code != http.StatusConflict {
		t.Fatalf("silent daemon: %d", rec.Code)
	}
}

func TestCommitLookupRepliesAndBounds(t *testing.T) {
	fx := newCommitFx(t)
	owner := fx.token(topicOwner, "human")
	summary := "/api/sessions/" + fx.sess + "/commits/0123456"
	diff := summary + "/diff"
	patch := "commit " + fixtureSHA + "\n\ndiff --git a/a.go b/a.go\n"
	rec := fx.answered(diff, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		if !q.Patch {
			t.Error("diff asked as a summary")
		}
		return protocol.CommitResult{OK: true, PatchB64: base64.StdEncoding.EncodeToString([]byte(patch))}
	})
	if rec.Code != http.StatusOK || rec.Body.String() != patch || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("diff: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	codes := []struct {
		path string
		code string
		want int
	}{
		{summary, "not_found", 404}, {summary, "invalid", 404}, {diff, "too_large", 413}, {summary, "busy", 429}, {summary, "unavailable", 409},
	}
	for _, c := range codes {
		rec := fx.answered(c.path, owner, func(protocol.ReadCommit) protocol.CommitResult { return protocol.CommitResult{ErrorCode: c.code} })
		if rec.Code != c.want || strings.Contains(rec.Body.String(), c.code+"\"") {
			t.Fatalf("%s: %d %s", c.code, rec.Code, rec.Body)
		}
	}
	bad := []func(*protocol.CommitInfo){
		func(c *protocol.CommitInfo) { c.SHA = strings.Repeat("f", 40) },            // not the commit asked for
		func(c *protocol.CommitInfo) { c.Short = "fffffff" },                        // short not of the sha
		func(c *protocol.CommitInfo) { c.Body = strings.Repeat("b", 5000) },         // over the body cap
		func(c *protocol.CommitInfo) { c.Parents = []string{"HEAD"} },               // not a sha
		func(c *protocol.CommitInfo) { c.Files = make([]protocol.CommitFile, 301) }, // over the file cap
		func(c *protocol.CommitInfo) { c.Files[0].Adds = -1 },
	}
	for i, change := range bad {
		rec := fx.answered(summary, owner, func(q protocol.ReadCommit) protocol.CommitResult {
			r := okCommit(q)
			change(r.Commit)
			return r
		})
		if rec.Code != http.StatusConflict {
			t.Fatalf("bad reply %d accepted: %d %s", i, rec.Code, rec.Body)
		}
	}
	over := base64.StdEncoding.EncodeToString(make([]byte, protocol.CommitPatchMaxBytes+1))
	if rec := fx.answered(diff, owner, func(protocol.ReadCommit) protocol.CommitResult {
		return protocol.CommitResult{OK: true, PatchB64: over}
	}); rec.Code != http.StatusConflict {
		t.Fatalf("oversized diff accepted: %d", rec.Code)
	}
	// A reply after the session's runtime claim changed is not served.
	rec = fx.answered(summary, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		fx.dc.RuntimeTokenID = newUUID()
		return okCommit(q)
	})
	fx.dc.RuntimeTokenID = ""
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale runtime claim: %d", rec.Code)
	}
}

func TestCommitLookupCapsReadsInFlightPerSession(t *testing.T) {
	fx := newCommitFx(t)
	owner := fx.token(topicOwner, "human")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{}, commitPendingPerSession)
	for range commitPendingPerSession {
		go func() {
			_, _ = fx.api.hub.requestCommit(ctx, fx.dc, "someone-else", fx.sess, "0123456", false, 30*time.Second)
			waiting <- struct{}{}
		}()
	}
	for range commitPendingPerSession {
		<-fx.dc.send
	}
	if rec := fx.refused("/api/sessions/"+fx.sess+"/commits/0123456", owner); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the per-session cap: %d %s", rec.Code, rec.Body)
	}
	cancel()
	for range commitPendingPerSession {
		<-waiting
	}
}

func TestBranchGitUsesLocalBrokerAndOwnerChecks(t *testing.T) {
	fx := newCommitFx(t)
	path := "/api/sessions/" + fx.sess + "/git?branch=work/card&base=main"
	owner := fx.token(topicOwner, "human")
	rec := fx.answered(path, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		if q.Branch != "work/card" || q.Base != "main" || q.SHA != "" {
			t.Fatalf("request %+v", q)
		}
		return protocol.CommitResult{OK: true, Branch: &protocol.BranchInfo{Name: q.Branch, Base: q.Base, Commits: []protocol.BranchCommit{{SHA: fixtureSHA, Message: "Local", Author: "Example", Date: "2026-10-01T00:00:00Z"}}}}
	})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Local") {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := fx.refused(path, fx.token(topicOther, "human")); rec.Code != 403 {
		t.Fatalf("nonowner: %d", rec.Code)
	}
	if rec := fx.refused("/api/sessions/"+fx.sess+"/git?branch=main..HEAD", owner); rec.Code != 422 {
		t.Fatalf("bad branch: %d", rec.Code)
	}
	rec = fx.answered("/api/sessions/"+fx.sess+"/git/diff?branch=work/card", owner, func(q protocol.ReadCommit) protocol.CommitResult {
		if !q.Patch {
			t.Fatal("expected diff")
		}
		return protocol.CommitResult{OK: true, PatchB64: base64.StdEncoding.EncodeToString([]byte("diff --git a/a b/a\n+local\n"))}
	})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "+local") {
		t.Fatalf("diff: %d %s", rec.Code, rec.Body)
	}
}

func TestBranchGitInvalidBoardProofCannotFallBackToStoredHuman(t *testing.T) {
	fx := newCommitFx(t)
	req := httptest.NewRequest("GET", "/api/sessions/"+fx.sess+"/git", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	req.Header.Set(desktopViewerHeader, "invalid-proof")
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("invalid proof fell back: %d", rec.Code)
	}
	select {
	case <-fx.dc.send:
		t.Fatal("read reached daemon")
	default:
	}
}

func TestBranchGitAcceptsOnlyVerifiedHumanBoardViewer(t *testing.T) {
	fx := newCommitFx(t)
	makeProof := func(account, kind string) string {
		return mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: account, Aud: "blerg-board", Kind: kind, Sid: testSID, Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	}
	path := "/api/sessions/" + fx.sess + "/git?branch=work/card"
	for _, tc := range []struct {
		account, kind string
		code          int
	}{{topicOther, "human", 403}, {topicOwner, "agent", 403}, {topicOwner, "human", 200}} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer stored-automation")
		req.Header.Set(desktopViewerHeader, makeProof(tc.account, tc.kind))
		rec := httptest.NewRecorder()
		if tc.code == 200 {
			done := make(chan struct{})
			go func() {
				defer close(done)
				var q protocol.ReadCommit
				select {
				case raw := <-fx.dc.send:
					if json.Unmarshal(raw, &q) != nil {
						return
					}
				case <-time.After(3 * time.Second):
					return
				}
				fx.api.hub.deliverCommitResult(fx.dc, protocol.CommitResult{Type: "commit_result", RequestID: q.RequestID, SessionID: q.SessionID, WorkspaceGitRef: q.WorkspaceGitRef, OK: true, Branch: &protocol.BranchInfo{Name: q.Branch, Commits: []protocol.BranchCommit{}}})
			}()
			fx.mux.ServeHTTP(rec, req)
			<-done
		} else {
			fx.mux.ServeHTTP(rec, req)
		}
		if rec.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.account, tc.kind, rec.Code, rec.Body)
		}
	}
}

func TestBranchGitExplainsOlderDaemonReply(t *testing.T) {
	fx := newCommitFx(t)
	rec := fx.answered("/api/sessions/"+fx.sess+"/git?branch=work/card", fx.token(topicOwner, "human"), func(q protocol.ReadCommit) protocol.CommitResult {
		return protocol.CommitResult{OK: true, Commit: &protocol.CommitInfo{SHA: fixtureSHA}}
	})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Update") {
		t.Fatalf("old daemon: %d %s", rec.Code, rec.Body)
	}
}

func TestBranchGitExplainsMissingLocalObjects(t *testing.T) {
	fx := newCommitFx(t)
	rec := fx.answered("/api/sessions/"+fx.sess+"/git/diff?branch=work/card", fx.token(topicOwner, "human"), func(q protocol.ReadCommit) protocol.CommitResult {
		return protocol.CommitResult{ErrorCode: "objects_unavailable"}
	})
	if rec.Code != 424 || !strings.Contains(rec.Body.String(), "Blobless") {
		t.Fatalf("missing objects: %d %s", rec.Code, rec.Body)
	}
}

func TestCommitFileRouteOwnerAndVersions(t *testing.T) {
	fx := newCommitFx(t)
	path := "/api/sessions/" + fx.sess + "/commits/0123456/file?path=src%2Fa.txt&side=original"
	owner := fx.token(topicOwner, "human")
	rec := fx.answered(path, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		if q.FilePath != "src/a.txt" || !q.Original || !q.Patch {
			t.Errorf("file request: %+v", q)
		}
		return protocol.CommitResult{OK: true, File: true, PatchB64: base64.StdEncoding.EncodeToString([]byte("whole original file\n"))}
	})
	if rec.Code != 200 || rec.Body.String() != "whole original file\n" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("file: %d %s", rec.Code, rec.Body)
	}
	rec = fx.answered(path, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		return protocol.CommitResult{OK: true, PatchB64: base64.StdEncoding.EncodeToString([]byte("old daemon diff"))}
	})
	if rec.Code != 409 {
		t.Fatalf("old daemon accepted: %d", rec.Code)
	}
	if rec = fx.refused(path, fx.token(topicOther, "human")); rec.Code != 403 {
		t.Fatalf("non-owner: %d", rec.Code)
	}
	if rec = fx.refused("/api/sessions/"+fx.sess+"/commits/0123456/file?path=../secret&side=changed", owner); rec.Code != 422 {
		t.Fatalf("invalid path: %d", rec.Code)
	}
}

func TestCommitBrokerTimeoutHasSafeCause(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 1)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	_, err := h.requestCommitRead(t.Context(), dc, "owner", protocol.ReadCommit{SessionID: "session", BoardHistory: true}, 0)
	if !errors.Is(err, errCommitUnavailable) || boardCodeErrorCause(err) != "commit_reply_timeout" {
		t.Fatalf("timeout lost its cause: %v (%s)", err, boardCodeErrorCause(err))
	}
}
