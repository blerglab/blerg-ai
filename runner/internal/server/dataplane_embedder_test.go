package server

import "testing"

// With VOYAGE_API_KEY unset nothing may ever be sent to Voyage: no embedder is
// built, so memory writes and knowledge search stay local (keyword fallback).
func TestNewEmbedderFromEnv_UnsetIsDisabled(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("VOYAGE_MODEL", "voyage-something")
	if e := NewEmbedderFromEnv(); e != nil {
		t.Fatalf("embedder built without VOYAGE_API_KEY: %#v", e)
	}
}

func TestNewEmbedderFromEnv_ModelDefault(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "k")
	t.Setenv("VOYAGE_MODEL", "")
	v, ok := NewEmbedderFromEnv().(*voyageEmbedder)
	if !ok || v.model != "voyage-3-lite" {
		t.Fatalf("got %#v, want voyage-3-lite default", v)
	}
	t.Setenv("VOYAGE_MODEL", "voyage-3")
	if v := NewEmbedderFromEnv().(*voyageEmbedder); v.model != "voyage-3" {
		t.Fatalf("model override ignored: %q", v.model)
	}
}
