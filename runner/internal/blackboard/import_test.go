package blackboard

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLegacyImportsCannotFinalize(t *testing.T) {
	type importer interface {
		Import(context.Context, Actor, []ImportedRelease) error
	}
	if _, ok := any(Service{}).(importer); ok {
		t.Fatal("legacy Import still exposes a Final writer")
	}
	// Existing historical payloads remain readable without becoming new consent.
	var evidence ImportedRelease
	if err := json.Unmarshal([]byte(`{"tag":"v0.5.0","date":"2026-10-28T00:00:00Z","notes":"Historical evidence","commit":"abc123"}`), &evidence); err != nil || evidence.Tag != "v0.5.0" {
		t.Fatal("legacy evidence lost")
	}
}
