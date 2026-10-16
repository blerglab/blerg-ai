package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A browser's initial_state carries the release version and git commit the server was built
// from, beside the server version, so the sidebar can show "Blerg v0.2.0 · 510d0c0".
func TestInitialStateCarriesReleaseAndCommit(t *testing.T) {
	hub, _, _, bc := sidebarHubFixture(t)
	hub.SetServerVersion("510d0c0")
	hub.SetServerBuild("0.2.0", "510d0c0")
	sendInitialState(context.Background(), bc, hub, nil)

	var st protocol.InitialState
	if err := json.Unmarshal(<-bc.send, &st); err != nil {
		t.Fatal(err)
	}
	if st.ServerVersion != "510d0c0" || st.ServerRelease != "0.2.0" || st.ServerCommit != "510d0c0" {
		t.Fatalf("server version/release/commit = %q/%q/%q", st.ServerVersion, st.ServerRelease, st.ServerCommit)
	}
}
