package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Reproduces the single-use rotation hazard with the pinned native consumer,
// without reading a real login or making any upstream provider request.
func TestCodexNativeCopiedRotation(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for isolated native rotation fixture")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("native Codex unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "testdata/native_rotation.py")
	cmd.Env = append(os.Environ(), "CODEX_ROTATION_FIXTURE_ROOT="+t.TempDir())
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal("native rotation fixture failed (output withheld)")
	}
	var result struct {
		Version string `json:"version"`
		Desktop struct {
			Rotated bool `json:"rotated_file"`
		} `json:"desktop"`
		Copy struct {
			Rotated bool `json:"rotated_file"`
		} `json:"copied_pod"`
		Provider struct {
			Reused int `json:"reused"`
		} `json:"provider"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Desktop.Rotated || result.Copy.Rotated || result.Provider.Reused < 1 {
		t.Fatal("native copied-token rotation hazard not reproduced")
	}
	t.Logf("%s: private login rotated; copied login could not rotate; provider rejected reuse", result.Version)
}

func TestCodexNativeAccessOnlySnapshot(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for isolated native snapshot fixture")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex unavailable")
	}
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "fixture-user", "email": "fixture@example.test", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "fixture-account", "chatgpt_plan_type": "pro"}})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": token, "id_token": token, "account_id": "fixture-account", "refresh_token": "fixture-private-refresh"}})
	native, err := NativeFile(raw, time.Now())
	if err != nil {
		t.Fatal("native snapshot preparation failed")
	}
	root := t.TempDir()
	if os.WriteFile(filepath.Join(root, "auth.json"), native, 0600) != nil {
		t.Fatal("native snapshot write failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "testdata/native_snapshot.py")
	cmd.Env = append(os.Environ(), "CODEX_SNAPSHOT_FIXTURE_ROOT="+root, "CODEX_SNAPSHOT_FIXTURE_BINARY="+binary)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("native access-only fixture failed (output withheld)")
	}
	var result struct {
		Native    bool `json:"native_account"`
		Missing   bool `json:"missing_slot_account"`
		Refreshes int  `json:"refreshes"`
	}
	if json.Unmarshal(out, &result) != nil || !result.Native || result.Missing || result.Refreshes != 0 {
		t.Fatal("native snapshot was unusable or attempted refresh")
	}
}
