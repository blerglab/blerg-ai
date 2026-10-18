package server

import (
	"context"
	"encoding/json"
	"strings"
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

// initial_state names a newer public release only when the hub knows of one.
func TestInitialStateCarriesLatestRelease(t *testing.T) {
	hub, _, _, bc := sidebarHubFixture(t)
	sendInitialState(context.Background(), bc, hub, nil)
	raw := <-bc.send
	if strings.Contains(string(raw), "latest_") {
		t.Fatalf("no feed, but initial_state has latest_ keys: %s", raw)
	}

	hub.SetLatestRelease(func() (string, string) { return "0.4.1", "https://github.com/o/r/releases/tag/v0.4.1" })
	sendInitialState(context.Background(), bc, hub, nil)
	var st protocol.InitialState
	if err := json.Unmarshal(<-bc.send, &st); err != nil {
		t.Fatal(err)
	}
	if st.LatestVersion != "0.4.1" || st.LatestURL != "https://github.com/o/r/releases/tag/v0.4.1" {
		t.Fatalf("latest = %q %q", st.LatestVersion, st.LatestURL)
	}
}
