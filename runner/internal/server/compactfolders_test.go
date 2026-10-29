package server

import (
	"encoding/json"
	"testing"
)

func TestCompactFoldersPreferenceValidation(t *testing.T) {
	for _, raw := range []string{"true", "false"} {
		got, err := validatePreference("compact_folders", json.RawMessage(raw))
		if err != nil || string(got) != raw {
			t.Errorf("%s: got %s, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"null", "0", `"false"`, "{}", "[]"} {
		if _, err := validatePreference("compact_folders", json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestCompactFoldersPreferenceRoundTrip(t *testing.T) {
	fx := prefsFx(t)
	human := fx.human()
	if w := callPrefs(fx, "PUT", human, `{"compact_folders":false}`); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	w := callPrefs(fx, "GET", human, "")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || string(got["compact_folders"]) != "false" {
		t.Fatalf("GET: %s, %v", w.Body.String(), err)
	}
	if w := callPrefs(fx, "PUT", human, goodColours); w.Code != 200 {
		t.Fatalf("colors PUT: %d", w.Code)
	}
	w = callPrefs(fx, "GET", human, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || string(got["compact_folders"]) != "false" {
		t.Fatalf("GET after colors: %s", w.Body.String())
	}
}
