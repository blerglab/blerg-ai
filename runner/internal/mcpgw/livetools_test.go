package mcpgw

import (
	"context"
	"errors"
	"testing"
)

func TestLiveToolHashes(t *testing.T) {
	h := newHarness(t, nil)
	proof := Proof{AccountID: "acct-1", SessionID: "login-1"}

	got, err := h.gw.LiveToolHashes(context.Background(), proof, "conn-1", h.up.url())
	if err != nil {
		t.Fatalf("LiveToolHashes: %v", err)
	}
	for _, name := range []string{"echo", "secret", "send"} {
		if got[name] == "" || got[name] != h.up.hash(name) {
			t.Errorf("hash of %s = %q, want %q", name, got[name], h.up.hash(name))
		}
	}
	if h.core.lastProof != proof || h.core.lastConn != "conn-1" {
		t.Errorf("core saw proof %+v connection %q", h.core.lastProof, h.core.lastConn)
	}

	// A description changed upstream shows up at once: nothing is cached across calls.
	h.up.setDescription("echo", "now does something else")
	again, err := h.gw.LiveToolHashes(context.Background(), proof, "conn-1", h.up.url())
	if err != nil {
		t.Fatal(err)
	}
	if again["echo"] == got["echo"] || again["echo"] != h.up.hash("echo") {
		t.Errorf("echo hash did not follow the upstream: %q", again["echo"])
	}

	// A connection edited to another host is refused by the snapshot check.
	if _, err := h.gw.LiveToolHashes(context.Background(), proof, "conn-1", "https://elsewhere.example/mcp"); err == nil {
		t.Error("a URL on a different host than the credential's was accepted")
	}

	// Core not knowing the connection surfaces as ErrConnectionGone.
	h.core.err = ErrConnectionGone
	if _, err := h.gw.LiveToolHashes(context.Background(), proof, "conn-1", h.up.url()); !errors.Is(err, ErrConnectionGone) {
		t.Errorf("err = %v, want ErrConnectionGone", err)
	}

	if _, err := h.gw.LiveToolHashes(context.Background(), Proof{AccountID: "a"}, "c", h.up.url()); err == nil {
		t.Error("a call without a proof was accepted")
	}
}
