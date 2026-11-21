package livedata

import (
	"strings"
	"testing"
)

func TestScopeGuardBoundaries(t *testing.T) {
	for name, mutate := range map[string]func(*Scope){
		"uuid length":     func(s *Scope) { s.Owner = "" },
		"uuid separator":  func(s *Scope) { s.Owner = strings.Replace(s.Owner, "-", "a", 1) },
		"uuid alphabet":   func(s *Scope) { s.Owner = "z" + s.Owner[1:] },
		"hash encoding":   func(s *Scope) { s.Hash = strings.Repeat("z", 64) },
		"hash length":     func(s *Scope) { s.Hash = "ab" },
		"hash case":       func(s *Scope) { s.Hash = strings.Repeat("A", 64) },
		"empty tool":      func(s *Scope) { s.Tool = "" },
		"long tool":       func(s *Scope) { s.Tool = strings.Repeat("a", 129) },
		"tool alphabet":   func(s *Scope) { s.Tool = "quotes/secret" },
		"revision":        func(s *Scope) { s.Revision = 0 },
		"generation":      func(s *Scope) { s.ConnectionGeneration = 0 },
		"owner epoch":     func(s *Scope) { s.OwnerEpoch = 0 },
		"authority epoch": func(s *Scope) { s.AuthorityEpoch = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s := scope()
			mutate(&s)
			if s.Valid() {
				t.Fatal("invalid authority scope accepted")
			}
		})
	}
}

func TestKeyIDGuardBoundaries(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("a", 65), "key/remote", "key.1", "kéy"} {
		if validKeyID(id) {
			t.Fatal("invalid key identifier accepted")
		}
	}
	if !validKeyID("key-1_A") {
		t.Fatal("valid local key identifier refused")
	}
}

func TestStrictTokenEncodingBoundaries(t *testing.T) {
	for _, raw := range []string{`{"alg":"EdDSA","typ":"blerg-live-data+jwt","kid":"key-1","remote":true}`, `{"alg":"EdDSA","typ":"blerg-live-data+jwt","kid":"key-1"} {}`, `{"alg":"EdDSA","typ":"blerg-live-data+jwt","kid":"key-1"} `, `null`, `{`} {
		var h header
		if strict([]byte(raw), &h) == nil {
			t.Fatal("ambiguous token encoding accepted")
		}
	}
}
