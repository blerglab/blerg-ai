package codexauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeFileKeepsRefreshAuthorityOutOfDelivery(t *testing.T) {
	raw := []byte(`{"tokens":{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":"fixture-id"}}`)
	snapshot, err := Snapshot(raw, time.Now())
	if err != nil || strings.Contains(string(snapshot), "refresh_token") {
		t.Fatal("refresh field in delivery snapshot")
	}
	native, err := NativeFile(raw, time.Now())
	var auth struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err != nil || json.Unmarshal(native, &auth) != nil || auth.Tokens["access_token"] != "fixture-access" {
		t.Fatal("native credential lost access")
	}
	value, present := auth.Tokens["refresh_token"]
	if !present || value != "" || strings.Contains(string(native), "fixture-refresh") {
		t.Fatal("native schema slot missing or granted refresh authority")
	}
	native, err = NativeFile([]byte(`{"OPENAI_API_KEY":"fixture-api"}`), time.Now())
	if err != nil || strings.Contains(string(native), "refresh_token") {
		t.Fatal("API-key file gained a token bundle")
	}
}
