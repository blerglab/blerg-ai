package engineaccess

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixtureAccess(mode string) Access {
	return Access{Mode: mode, AccessToken: "fixture-access", AccountID: "fixture-account", PlanType: "plus", Generation: "fixture-generation", ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func TestCodexWireRemainsCompatible(t *testing.T) {
	raw, err := json.Marshal(fixtureAccess("chatgptAuthTokens"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"mode":"chatgptAuthTokens","access_token":"fixture-access","chatgpt_account_id":"fixture-account","plan_type":"plus","generation":"fixture-generation","expires_at":"2030-01-02T03:04:05Z"}`
	if string(raw) != want {
		t.Fatalf("Codex wire changed: %s", raw)
	}
	access, err := DecodeForEngine("codex", raw, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || access.AccountID != "fixture-account" {
		t.Fatal("Codex decoding failed")
	}
}

func TestRegisteredModeOwnsWireAndValidation(t *testing.T) {
	codec := TokenCodec("fixture-engine", "fixtureAccessTokens")
	codec.Validate = func(a Access) bool { return a.AccountID == "fixture-account" && a.PlanType == "plus" }
	t.Cleanup(Register(codec))
	raw, err := json.Marshal(fixtureAccess(codec.Mode))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "chatgpt_account_id") || !strings.Contains(string(raw), `"account_id":"fixture-account"`) {
		t.Fatal("second engine inherited Codex wire")
	}
	access, err := DecodeForEngine("fixture-engine", raw, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || access.AccessToken != "fixture-access" {
		t.Fatal("registered mode was refused")
	}
	if _, err = DecodeForEngine("codex", raw, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("foreign mode crossed engine binding")
	}
	bad := fixtureAccess(codec.Mode)
	bad.PlanType = "invalid"
	raw, _ = json.Marshal(bad)
	if _, err = DecodeForEngine("fixture-engine", raw, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("mode-specific validator was skipped")
	}
}

func TestAccessRejectsUnsafeEnvelopesWithoutProviderDetails(t *testing.T) {
	now := time.Now()
	valid := fixtureAccess("chatgptAuthTokens")
	valid.ExpiresAt = now.Add(time.Hour)
	for _, scenario := range []string{"unknown", "expired", "empty-token", "oversized-token", "empty-generation", "oversized-body", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			a := valid
			switch scenario {
			case "unknown":
				a.Mode = "unknown"
			case "expired":
				a.ExpiresAt = now.Add(-time.Second)
			case "empty-token":
				a.AccessToken = ""
			case "oversized-token":
				a.AccessToken = strings.Repeat("x", 25<<10)
			case "empty-generation":
				a.Generation = ""
			}
			raw, _ := json.Marshal(a)
			if scenario == "unknown" {
				raw = []byte(`{"mode":"unknown","access_token":"fixture-private"}`)
			}
			if scenario == "oversized-body" {
				raw = []byte(strings.Repeat("x", 33<<10))
			}
			if scenario == "malformed" {
				raw = []byte(`{"mode":"chatgptAuthTokens","access_token":"fixture-private"}`)
			}
			got, err := DecodeForEngine("codex", raw, now)
			if err == nil || got.AccessToken != "" || strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("unsafe managed envelope accepted or reflected")
			}
		})
	}
}
