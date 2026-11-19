//go:build !linux

package commandsteps

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUnsupportedPlatformRefusesFixedCommands(t *testing.T) {
	steps := []Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	d, err := Approve(steps, Hash(steps), "person", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), d, "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "only on Linux") {
		t.Fatalf("platform refusal: %v", err)
	}
}
