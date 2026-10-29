package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/jackc/pgx/v5"
)

func TestLegacyClusterExplorerDoesNotRequireDesktopOwnership(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "legacy-cluster")
	dc.DesktopBinding = nil
	dc.Compatibility = false
	dc.BoundSessionID, dc.RuntimeTokenID = f.sess, "fixture-runtime"
	f.api.hub.daemons[dc.ID] = dc
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='cluster',status='idle' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	err := f.api.ensureLegacyWorkspace(t.Context(), f.sess)
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "desktop") || err.Error() != "This session started without a workspace. Start a new cluster session to browse its files." {
		t.Fatal("legacy cluster should explain missing workspace binding")
	}
}

func TestLegacyWorkspaceAttachmentRecoveryAndRefusals(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "legacy-desktop")
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='daemon',status='idle' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	call := func(req protocol.LocalWorkspaceControl) protocol.LocalWorkspaceControlResult {
		req.SessionID, req.RequestID = f.sess, newUUID()
		var reply protocol.LocalWorkspaceControlResult
		if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
			reply = f.api.hub.localWorkspaceControl(ctx, f.pool, dc, req)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	req := protocol.LocalWorkspaceControl{Operation: "legacy_attach", Binding: protocol.WorkspaceRuntimeBinding{Root: protocol.WorkspaceRoot{Device: 1, Inode: 7}}}
	first := call(req)
	if !first.OK || first.Manifest == nil {
		t.Fatalf("old-protocol session cannot attach: %+v", first)
	}
	m, gen, err := loadExplorerBinding(t.Context(), f.pool, f.sess)
	if err != nil || gen != 1 || m.OwnerID != topicOwner || m.Runtime != "host" || len(m.Members) != 1 || m.Members[0].Path != "." {
		t.Fatalf("legacy binding: %+v %d %v", m, gen, err)
	}
	selection, err := db.GetSessionWorkspace(t.Context(), f.pool, f.sess)
	if err != nil || selection.Manifest != nil {
		t.Fatal("attachment became a launch selection")
	}
	if retry := call(req); !retry.OK || retry.Launch != first.Launch {
		t.Fatal("exact retry changed attachment")
	}
	req.Binding.Root.Inode++
	if call(req).OK {
		t.Fatal("replacement project directory accepted")
	}
	req.Binding.Root.Inode--
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilities{}
	if call(req).OK {
		t.Fatal("old peer gained capabilities")
	}
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	dc.DesktopGeneration = newUUID()
	if err := db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
		return db.BindDesktopConnection(ctx, f.pool, *dc.DesktopBinding, dc.Name, dc.Mode, dc.ReposRoot, dc.DesktopGeneration)
	}); err != nil {
		t.Fatal(err)
	}
	recovered := call(req)
	if !recovered.OK || recovered.Launch.ConnectionID != dc.DesktopGeneration || recovered.Launch.RuntimeGeneration != gen {
		t.Fatal("reconnect did not reauthorize exact root")
	}
	if _, err := localWorkspaceTerminalContext(t.Context(), f.pool, f.sess, protocol.SessionEnded{SessionID: f.sess}); err != nil {
		t.Fatal("attachment hijacked legacy completion", err)
	}

	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET restrict_tools=true,private=true WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	attachment := call(req)
	if !attachment.OK || attachment.ShellEligible {
		t.Fatal("restricted session gained a shell or private owner lost explorer")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET restrict_tools=false,private=false WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, f.sess, topicOther); err != nil {
		t.Fatal(err)
	}
	if call(req).OK {
		t.Fatal("changed owner accepted")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, f.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),status='stopped' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	if call(req).OK {
		t.Fatal("ended session accepted")
	}
}

func TestLegacyShellRequestsRequireOwnerBeforeRuntime(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "legacy-owner-desktop")
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
	dc.shellControl = make(chan []byte, 32)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='daemon',status='idle' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	f.api.hub.SetSessionOwner(f.sess, dc.ID)
	f.api.shellRuntime = newRemoteShell(f.api)
	manager, err := ownershell.New(shellTestAuthority{}, shellResolver{f.api, f.api.shellRuntime}, f.api.shellRuntime, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.api.shells = manager
	f.api.RegisterShellRoutes(f.mux)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case raw := <-dc.shellControl:
				calls.Add(1)
				var req shellruntime.RPC
				if json.Unmarshal(raw, &req) == nil {
					f.api.hub.deliverShell(dc, shellruntime.RPC{Type: "owner_shell_result", Version: 1, SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, ErrorCode: "unavailable"})
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	for _, endpoint := range []struct {
		method, path string
		status       int
	}{
		{"GET", "shell-capability", 200}, {"POST", "shell-consent/challenge", 403}, {"POST", "shell-consent/accept", 403}, {"GET", "shells", 403}, {"POST", "shells", 403},
	} {
		t.Run(endpoint.method+"/"+endpoint.path, func(t *testing.T) {
			req := httptest.NewRequest(endpoint.method, "http://shell.example/api/sessions/"+f.sess+"/"+endpoint.path, strings.NewReader(""))
			req.Header.Set("Authorization", "Bearer "+f.token(topicOther, "human", "account.manage"))
			req.Header.Set("Origin", "http://shell.example")
			rec := httptest.NewRecorder()
			before := calls.Load()
			f.mux.ServeHTTP(rec, req)
			if rec.Code != endpoint.status {
				t.Errorf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if calls.Load() != before {
				t.Error("non-owner request dispatched attachment or describe RPC")
			}
			ws, err := db.GetSessionReaderWorkspace(t.Context(), f.pool, f.sess)
			if err != nil || ws == nil || ws.Manifest != nil {
				t.Error("non-owner request changed reader authority", err)
			}
			generation, binding, err := db.GetWorkspaceRuntime(t.Context(), f.pool, f.sess)
			if !errors.Is(err, pgx.ErrNoRows) || generation != 0 || binding != nil {
				t.Error("non-owner request changed runtime state", err)
			}
		})
	}
	// Prove the fixture reaches the attachment path for the requesting owner.
	req := httptest.NewRequest("GET", "http://shell.example/api/sessions/"+f.sess+"/shell-capability", nil)
	req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
	before := calls.Load()
	f.mux.ServeHTTP(httptest.NewRecorder(), req)
	if calls.Load() != before+1 {
		t.Fatal("owner did not reach attachment")
	}
}
