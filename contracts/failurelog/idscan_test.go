package failurelog

import (
	"strings"
	"testing"
)

func TestIDScanner(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error_id":"abcdef12"}`, "abcdef12"},
		{`{"error":"` + strings.Repeat("CANARY_SECRET", 10000) + `","error_id":"abcdef12"}`, "abcdef12"},
		{`{"nested":{"error_id":"abcdef12"},"error":"CANARY_SECRET"}`, ""},
		{`{"error":"\"error_id\":\"abcdef12\""}`, ""},
		{`{"error_id":"CANARY_SECRET"}`, ""},
		{`{"error_id":null,"error":"abcdef12"}`, ""},
		{`{"error_id":{"error_id":"abcdef12"}}`, ""},
		{`{"\u0065rror_id":"\u0061bcdef12"}`, "abcdef12"},
		{`{"error_id":"abcdef12"} extra`, ""},
		{`{"error_id":"abcdef12"`, ""},
		{`["error_id","abcdef12"]`, ""},
	} {
		var s idScanner
		for i := range len(tc.body) {
			s.write([]byte{tc.body[i]})
		}
		if got := s.errorID(); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
