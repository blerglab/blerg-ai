package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineDisplayNames(t *testing.T) {
	got := engineDisplayNames()
	want := "Claude, Codex, Hermes, or OpenClaw"
	if got != want {
		t.Errorf("engineDisplayNames() = %q, want %q", got, want)
	}
}

func TestEngineRegistryLookup(t *testing.T) {
	for _, id := range engineOrder {
		spec, ok := engineRegistry[id]
		if !ok {
			t.Fatalf("engineOrder lists %q but engineRegistry has no entry for it", id)
		}
		if spec.ID != id {
			t.Errorf("engineRegistry[%q].ID = %q, want %q", id, spec.ID, id)
		}
		if spec.Binary == "" || spec.NewAgentDriver == nil {
			t.Errorf("engineRegistry[%q] missing Binary or NewAgentDriver", id)
		}
	}
	if _, ok := engineRegistry[""]; ok {
		t.Error(`engineRegistry must not have an entry for "" — that's the Claude fallback, handled outside the registry`)
	}
}

// TestAvailableEngineDetection exercises the config-file half of each
// *AlreadyConfigured/*AlreadyLoggedIn check by pointing HOME at a scratch
// directory. The binary-presence half only looks the CLI up on PATH, so a
// scratch PATH of empty stubs stands in for all four; the result then does
// not depend on which engines the machine running the test has installed.
func TestAvailableEngineDetection(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skip("needs a real filesystem for HOME redirection")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex", "hermes", "openclaw"} {
		mustWrite(t, filepath.Join(bin, name), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	if claudeAvailable() {
		t.Error("claudeAvailable() = true with no ~/.claude/.credentials.json")
	}
	if codexAlreadyLoggedIn() {
		t.Error("codexAlreadyLoggedIn() = true with no ~/.codex/auth.json")
	}
	if hermesAlreadyConfigured() {
		t.Error("hermesAlreadyConfigured() = true with no ~/.hermes config")
	}
	if openclawAlreadyConfigured() {
		t.Error("openclawAlreadyConfigured() = true with no ~/.openclaw state")
	}

	mustWrite(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`)
	if !claudeAvailable() {
		t.Error("claudeAvailable() = false with a populated credentials file")
	}

	mustWrite(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"x"}`)
	if !codexAlreadyLoggedIn() {
		t.Error("codexAlreadyLoggedIn() = false with a non-empty auth.json")
	}

	// Provider key alone is sufficient.
	mustWrite(t, filepath.Join(home, ".hermes", ".env"), "OPENROUTER_API_KEY=sk-test\n")
	if !hermesAlreadyConfigured() {
		t.Error("hermesAlreadyConfigured() = false with a provider key in .env")
	}

	// A config.yaml base_url alone is also sufficient, even with no key.
	os.Remove(filepath.Join(home, ".hermes", ".env"))
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "base_url: http://localhost:11434/v1\n")
	if !hermesAlreadyConfigured() {
		t.Error("hermesAlreadyConfigured() = false with a base_url in config.yaml and no key")
	}

	mustWrite(t, filepath.Join(home, ".openclaw", "state", "openclaw.sqlite"), "not-really-sqlite-but-non-empty")
	if !openclawAlreadyConfigured() {
		t.Error("openclawAlreadyConfigured() = false with a non-empty openclaw.sqlite")
	}

	got := availableEngines()
	want := map[string]bool{"claude": true, "codex": true, "hermes": true, "openclaw": true}
	if len(got) != len(want) {
		t.Fatalf("availableEngines() = %v, want all four engines", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("availableEngines() returned unexpected id %q", id)
		}
	}
	if got[0] != "claude" {
		t.Errorf("availableEngines()[0] = %q, want claude first", got[0])
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildTerminalCommand(t *testing.T) {
	for _, tc := range []struct {
		name            string
		spec            EngineSpec
		model           string
		skipPerms       bool
		wantAfterBinary []string
	}{
		{
			name:            "codex: model + skip-perms flags",
			spec:            engineRegistry["codex"],
			model:           "gpt-5-codex",
			skipPerms:       true,
			wantAfterBinary: []string{"--model", "gpt-5-codex", "--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:            "codex: no model, no skip-perms",
			spec:            engineRegistry["codex"],
			wantAfterBinary: nil,
		},
		{
			name:            "hermes: model + skip-perms flags",
			spec:            engineRegistry["hermes"],
			model:           "anthropic/claude-sonnet-4.6",
			skipPerms:       true,
			wantAfterBinary: []string{"-m", "anthropic/claude-sonnet-4.6", "--yolo"},
		},
		{
			name:            "openclaw: ExtraArgs only — no ModelFlag/SkipPermsFlag exist",
			spec:            engineRegistry["openclaw"],
			model:           "mybox/qwen3-30b",
			skipPerms:       true,
			wantAfterBinary: []string{"chat"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTerminalCommand(tc.spec, tc.model, "", tc.skipPerms, false)
			if got := got[1:]; !slicesEqual(got, tc.wantAfterBinary) {
				t.Errorf("buildTerminalCommand(...) args[1:] = %v, want %v", got, tc.wantAfterBinary)
			}
		})
	}
}

func TestBuildTerminalCommandSandboxed(t *testing.T) {
	got := buildTerminalCommand(engineRegistry["hermes"], "", "", false, true)
	if got[0] != "hermes" {
		t.Errorf("sandboxed binary = %q, want bare %q", got[0], "hermes")
	}
}
