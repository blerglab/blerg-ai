package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cluster whose Secret has no engine credentials must report an empty list, not null: the
// Cluster page reads the list and a null used to blank the whole page.
func TestClusterStatusReportsEmptyEngineListNotNull(t *testing.T) {
	jm := newTestJobManager(t, &fakeK8s{})
	raw, err := json.Marshal(jm.Status())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"available_engines":null`) {
		t.Fatalf("available_engines is null: %s", raw)
	}
	var s struct {
		Engines []string `json:"available_engines"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Engines == nil {
		t.Fatalf("available_engines must be a list: %s", raw)
	}
}
