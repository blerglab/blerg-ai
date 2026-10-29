package server

import (
	"encoding/json"
	"testing"
)

func TestDesktopEnrollmentRefreshRetainsScope(t *testing.T) {
	f := newPrivFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=true WHERE id=$1`, f.dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO desktop_compatibility_accounts(desktop_id,account_id) VALUES($1,$2)`, f.dc.ID, privAcctA); err != nil {
		t.Fatal(err)
	}
	bc := &BrowserConn{AccountID: privAcctA, send: make(chan []byte, 16), ctx: t.Context()}
	before, err := f.hub.desktopScopeRows(t.Context(), privAcctA)
	if err != nil {
		t.Fatal(err)
	}
	// One enrollment commits ownership/credential revision and compatibility together.
	if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=ownership_revision+1,desktop_compatibility=false WHERE id=$1`, f.dc.ID, privAcctA); err != nil {
		t.Fatal(err)
	}
	after := f.hub.refreshDesktopScope(t.Context(), bc, f.pool, before)
	if after == before {
		t.Fatal("enrollment did not refresh inventory")
	}
	var message map[string]any
	if err := json.Unmarshal(<-bc.send, &message); err != nil {
		t.Fatal(err)
	}
	if message["type"] != "initial_state" {
		t.Fatalf("retained access caused destructive reset: %v", message["type"])
	}
	if len(bc.send) != 0 {
		t.Fatal("enrollment emitted duplicate refreshes")
	}
	// Reconnection/status updates do not change authority or require another snapshot.
	if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET connection_generation=$2,status='disconnected',status_reason='Reconnecting' WHERE id=$1`, f.dc.ID, newUUID()); err != nil {
		t.Fatal(err)
	}
	f.hub.refreshDesktopScope(t.Context(), bc, f.pool, after)
	if len(bc.send) != 0 {
		t.Fatal("reconnect caused another scope refresh")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE daemons SET status='refused',status_reason='Enroll this desktop.' WHERE id=$1`, f.dc.ID); err != nil {
		t.Fatal(err)
	}
	if f.hub.refreshDesktopScope(t.Context(), bc, f.pool, after) == after || len(bc.send) != 1 {
		t.Fatal("desktop refusal diagnostics did not refresh")
	}
}
