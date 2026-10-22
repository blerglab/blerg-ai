package coreauth

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
)

func TestApprovalCapabilitiesAreSensitive(t *testing.T) {
	intent, digest := "12345678-1234-4321-8123-123456789abc", strings.Repeat("a", 64)
	card, _ := boardfiling.Capability(intent, digest)
	board, _ := boardfiling.BoardCapability(intent, digest)
	for _, c := range []string{card, board} {
		if !SensitiveCaps(c) {
			t.Fatalf("%s is not sensitive", c)
		}
	}
	if SensitiveCaps("card.read") {
		t.Fatal("card.read became sensitive")
	}
}
