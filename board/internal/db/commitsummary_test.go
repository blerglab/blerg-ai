package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommitSummaryMetadataOnly(t *testing.T) {
	valid := `[{"repository":"example-org/app","session_id":"0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55","branch":"work/card","base":"main","commits":[{"sha":"abcdef1234567890abcdef1234567890abcdef12","message":"Work","author":"Example","date":"2026-11-01T00:00:00Z"}],"files":[{"path":"app.go","adds":3,"dels":1}]}]`
	for _, input := range []string{valid, `[]`, ``} {
		if err := ValidateCommitSummary(json.RawMessage(input)); err != nil {
			t.Fatalf("valid: %v", err)
		}
	}
	for name, input := range map[string]string{
		"null": `null`, "object": `{}`, "patch": strings.Replace(valid, `"base":"main"`, `"patch":"private bytes","base":"main"`, 1),
		"negative stats": strings.Replace(valid, `"adds":3`, `"adds":-1`, 1),
		"absolute path":  strings.Replace(valid, `"path":"app.go"`, `"path":"/private/app.go"`, 1),
		"traversal":      strings.Replace(valid, `"path":"app.go"`, `"path":"../app.go"`, 1),
		"bad sha":        strings.Replace(valid, `abcdef1234567890abcdef1234567890abcdef12`, `--output=private`, 1),
		"bad date":       strings.Replace(valid, `2026-11-01T00:00:00Z`, `yesterday`, 1),
		"null commits":   strings.Replace(valid, `"commits":[{"sha":"abcdef1234567890abcdef1234567890abcdef12","message":"Work","author":"Example","date":"2026-11-01T00:00:00Z"}]`, `"commits":null`, 1),
		"stale member":   strings.Replace(valid, `"base":"main"`, `"base":"main","member_id":"member"`, 1),
		"oversize":       `[` + strings.Repeat(" ", 128<<10) + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateCommitSummary(json.RawMessage(input)) == nil {
				t.Fatal("accepted invalid metadata")
			}
		})
	}
}

func TestCommitSummaryUnsafeUnicode(t *testing.T) {
	for _, bad := range []string{"\u202e", "\u0085", "\u2028", "\u2029", string([]byte{0xff})} {
		raw := `[{"repository":"example-org/app","session_id":"0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55","branch":"` + bad + `","base":"main","commits":[],"files":[]}]`
		if ValidateCommitSummary(json.RawMessage(raw)) == nil {
			t.Errorf("accepted unsafe text %q", bad)
		}
	}
}
