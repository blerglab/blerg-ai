package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"testing"
	"time"
)

func TestHostRestartEndWithoutProcess(t *testing.T) {
	for _, status := range []string{"running", "idle", "waiting", "starting"} {
		t.Run(status, func(t *testing.T) {
			f := newOwnedEndFixture(t, status, 8)
			if code := f.delete(t); code != http.StatusNoContent {
				t.Fatalf("End without a process = %d, want 204", code)
			}
			row := f.row(t)
			if row.EndedAt == nil {
				t.Fatal("End did not durably end the session")
			}
			if stopped := reconcileSessions(t.Context(), f.hub, f.pool, f.dc.ID, []string{f.sessionID}, nil); len(stopped) != 1 {
				t.Fatal("reconnect revived an explicitly ended session")
			}
		})
	}
}

func TestHostRestartManagedEndWithoutDaemonRetainsCleanupIntent(t *testing.T) {
	f := newOwnedEndFixture(t, "running", 8)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "host", OwnerID: "user-1", LaunchGeneration: newUUID(), Digest: "fixture", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sessionID, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,'token',$2,1,'connection')`, f.sessionID, m.LaunchGeneration); err != nil {
		t.Fatal(err)
	}
	if code := f.delete(t); code != http.StatusServiceUnavailable {
		t.Errorf("unsettled managed End = %d, want 503", code)
	}
	var requested bool
	if err := f.pool.QueryRow(t.Context(), `SELECT stop_requested FROM local_workspace_authority WHERE session_id=$1`, f.sessionID).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("managed End bypassed durable workspace stop intent")
	}
}

// rc.4's legacy End fixture has neither a live login ID nor persisted ownership.
// New owner-only recovery and discard paths need both, without changing old tests.
func newOwnedEndFixture(t *testing.T, status string, sendBuf int) *endFixture {
	t.Helper()
	f := newEndFixture(t, status, sendBuf)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.api.coreAuth = newTestCoreAuthClient(t, pub, "recovery-core")
	f.mintAs = func(sub string) string {
		return mintRunnerToken(t, priv, "recovery-core", identity.Claims{Sub: sub, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	}
	f.bearer = f.mintAs("user-1")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='user-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	return f
}
