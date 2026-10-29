package server

import (
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func TestMain(m *testing.M) {
	// Existing websocket fixtures exercise the explicit legacy compatibility mode.
	// Desktop-authority tests explicitly disable it and use per-person credentials.
	if _, set := os.LookupEnv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS"); !set {
		_ = os.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "true")
	}
	os.Exit(testsupport.Run(m))
}
