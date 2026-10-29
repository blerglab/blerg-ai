package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type shellAuditReceipt chan ownershell.Audit

func (a shellAuditReceipt) Record(event ownershell.Audit) { a <- event }

type countedClusterShellRuntime struct {
	*shellTestRuntime
	resolves atomic.Int32
	opens    atomic.Int32
}

func (s *countedClusterShellRuntime) Resolve(ctx context.Context, id string) (ownershell.Binding, error) {
	s.resolves.Add(1)
	return s.shellTestRuntime.Resolve(ctx, id)
}
func (s *countedClusterShellRuntime) Open(ctx context.Context, b ownershell.Binding, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	s.opens.Add(1)
	return s.shellTestRuntime.Open(ctx, b, g, key)
}

// Reproduce the smoke's exact interval: all workspace receipts exist, but the
// engine has not yet reported a live state. A ready owner must then open normally.
func TestClusterOwnerShellStartingThenReady(t *testing.T) {
	f := newTopicFx(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET runtime='cluster',status='starting',private=true WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	b := shellTestBinding()
	b.Session, b.Owner, b.Runtime = f.sess, topicOwner, "cluster"
	manifest := protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: b.Owner, Runtime: b.Runtime, LaunchGeneration: b.Launch, Root: b.Root, Digest: b.ManifestDigest, Members: []protocol.WorkspaceMember{{ID: "member", Name: "project", Path: "project", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	if err := db.InsertSessionWorkspace(ctx, f.pool, f.sess, manifest); err != nil {
		t.Fatal(err)
	}
	generation, err := db.BeginWorkspaceRuntime(ctx, f.pool, f.sess, b.Launch, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: b.Launch, Generation: generation}
	b.RuntimeGeneration, b.Audience = generation, ownershell.RuntimeAudience(b.Connection, generation)
	if err := db.BindWorkspaceRuntime(ctx, f.pool, f.sess, ref, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, Root: b.Root, ManifestDigest: b.ManifestDigest}); err != nil {
		t.Fatal(err)
	}
	rt := &countedClusterShellRuntime{shellTestRuntime: &shellTestRuntime{binding: b, out: make(chan []byte, 8)}}
	// Live Core authorization verifies the authenticated owner's exact login.
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var actor struct {
			Owner string `json:"owner_id"`
			Login string `json:"login_id"`
		}
		if r.URL.Path != "/internal/shell/authorize" || r.Header.Get("X-Internal-Key") != "fixture-key" || json.NewDecoder(r.Body).Decode(&actor) != nil || actor.Owner != topicOwner || actor.Login != "login" {
			w.WriteHeader(403)
			return
		}
		writeJSON(w, 200, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	}))
	defer core.Close()
	audit := make(shellAuditReceipt, 128)
	m, err := ownershell.New(shellAuthority{core.URL, "fixture-key", core.Client()}, shellResolver{f.api, rt}, rt, audit, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.api.shells = m
	m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	defer m.Unavailable(context.Background())
	f.api.RegisterShellRoutes(f.mux)
	openSession := func(owner, id string) *httptest.ResponseRecorder {
		caps := []string{coreAuthBrowserCap}
		if owner != "unrelated-human" {
			caps = append(caps, "account.manage")
		}
		token := mintTestToken(t, f.priv, "core-1", identity.Claims{Kind: "human", Sub: owner, Sid: "login", Aud: coreAuthAudience, Caps: caps, ExpiresAt: time.Now().Add(time.Hour).Unix()})
		r := httptest.NewRequest("POST", "http://shell.example/api/sessions/"+id+"/shells", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", "http://shell.example")
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	open := func(owner string) *httptest.ResponseRecorder { return openSession(owner, f.sess) }
	r := open(topicOwner)
	if r.Code != 403 || !strings.Contains(r.Body.String(), `"code":"session_not_ready"`) {
		t.Fatalf("starting session: %d %s", r.Code, r.Body.String())
	}
	receipt := <-audit
	if receipt.Reason != ownershell.ReasonNotReady || receipt.Session != f.sess || receipt.Actor != topicOwner || receipt.Connection != "" || receipt.Launch != "" {
		t.Fatal("readiness refusal audit lost or invented runtime binding", receipt)
	}
	r = open(topicOther)
	if r.Code != 403 || strings.Contains(r.Body.String(), "session_not_ready") {
		t.Fatal("foreign actor learned session readiness", r.Code, r.Body.String())
	}
	beginStart(ctx, f.api.hub, f.pool, f.sess, "cluster", clusterWorkspaceStartPlan())
	if err := HandleSessionStateChanged(ctx, f.api.hub, f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: f.sess, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	r = open(topicOther)
	if r.Code != 403 || !strings.Contains(r.Body.String(), `"code":"unavailable"`) {
		t.Fatal("foreign actor can distinguish a ready private session", r.Code, r.Body.String())
	}

	// Neither ordinary humans nor admins gain private session metadata. The
	// response must be byte-identical across every persisted/runtime state.
	for _, actor := range []string{topicOther, "unrelated-human"} {
		for _, state := range []string{"missing", "starting", "disconnected", "running", "idle", "waiting", "policy_disabled", "fenced", "terminal"} {
			t.Run(actor+"/"+state, func(t *testing.T) {
				status := state
				if state == "missing" || state == "policy_disabled" || state == "fenced" {
					status = "idle"
				}
				if state == "terminal" {
					status = "stopped"
				}
				if _, e := f.pool.Exec(ctx, `UPDATE sessions SET status=$2 WHERE id=$1`, f.sess, status); e != nil {
					t.Fatal(e)
				}
				m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: state != "policy_disabled"})
				rt.mu.Lock()
				rt.binding.Live = state != "fenced"
				rt.mu.Unlock()
				id := f.sess
				if state == "missing" {
					id = "missing-private-session"
				}
				beforeResolves, beforeOpens := rt.resolves.Load(), rt.opens.Load()
				response := openSession(actor, id)
				if response.Code != 403 || response.Body.String() != "{\"code\":\"unavailable\",\"error\":\"shell unavailable\"}\n" {
					t.Fatal("unauthorized response distinguishes private session state", response.Code, response.Body.String())
				}
				if rt.resolves.Load() != beforeResolves || rt.opens.Load() != beforeOpens {
					t.Fatal("unauthorized actor dispatched to runtime")
				}
			})
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.binding.Live = true
	rt.mu.Unlock()
	m.SetPolicy(ctx, protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	stages, err := clusterLaunchSnapshot(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, stage := range stages {
		if stage.Stage == "ready" && stage.Outcome == "done" {
			found = true
		}
	}
	if !found {
		t.Fatal("live engine did not persist readiness", stages)
	}
	r = open(topicOwner)
	if r.Code != 201 {
		t.Fatalf("ready owner: %d %s", r.Code, r.Body.String())
	}
	advanceStart(ctx, f.api.hub, f.pool, f.sess, true, stage("shell_open", "Owner shell", protocol.StageStateActive, "Opening shell"))
	advanceStart(ctx, f.api.hub, f.pool, f.sess, true, stage("shell_open", "Owner shell", protocol.StageStateFailed, "Owner shell refused: session_not_ready"))
	stages, err = clusterLaunchSnapshot(ctx, f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	last := stages[len(stages)-1]
	if last.Stage != "shell_open" || last.Reason != "session_not_ready" || last.Outcome != "failed" || last.FinishedAt.IsZero() || last.StartedAt.IsZero() {
		t.Fatal("shell refusal diagnostic lost", last)
	}
}

func TestOwnerShellRefusalCodes(t *testing.T) {
	for _, reason := range []string{"binding_refused", "policy_unavailable", "policy_disabled", "authority_refused", "runtime_refused"} {
		t.Run(reason, func(t *testing.T) {
			rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 1)}
			authority := shellTestAuthority{}
			audit := make(shellAuditReceipt, 128)
			m, err := ownershell.New(authority, rt, rt, audit, nil)
			if err != nil {
				t.Fatal(err)
			}
			policy := protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}
			switch reason {
			case "binding_refused":
				rt.binding.Live = false
			case "policy_disabled":
				policy.Enabled = false
			case "authority_refused":
				policy.Revision = 2
			case "runtime_refused":
				rt.out = nil
			}
			if reason != "policy_unavailable" {
				m.SetPolicy(t.Context(), policy)
			}
			defer m.Unavailable(context.Background())
			_, err = m.Open(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}, "session")
			if !errors.Is(err, ownershell.ErrDenied) {
				t.Fatal("denial contract changed", err)
			}
			receipt := <-audit
			if string(receipt.Reason) != reason {
				t.Fatal("audit refusal lost", receipt)
			}
			w := httptest.NewRecorder()
			shellError(w, err)
			if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"`+reason+`"`) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, err := range []error{errors.New("fixture-private-token"), ownershell.Refuse(ownershell.Reason("fixture-private-token"))} {
		w := httptest.NewRecorder()
		shellError(w, err)
		if strings.Contains(w.Body.String(), "fixture-private-token") || !strings.Contains(w.Body.String(), `"code":"unavailable"`) {
			t.Fatal("unsafe refusal", w.Body.String())
		}
	}
}
