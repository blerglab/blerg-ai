package livemcp

import (
	"bytes"
	"strings"
	"testing"
)

func TestCanonicalObjectApprovalBoundary(t *testing.T) {
	canonical, e := CanonicalObject([]byte(`{ "b":2,"a":{"z":0} }`))
	if e != nil || !bytes.Equal(canonical, []byte(`{"a":{"z":0},"b":2}`)) {
		t.Fatal("unstable approval bytes", e)
	}
	for name, raw := range map[string]string{
		"array": `[]`, "null": `null`, "scalar": `1`, "duplicate root": `{"a":1,"a":2}`, "duplicate nested": `{"a":{"x":1,"x":2}}`, "trailing value": `{} {}`, "invalid JSON": `{`,
		"oversize": `{"symbol":"` + strings.Repeat("x", maxMappingBytes) + `"}`, "depth": strings.Repeat(`{"x":`, 70) + `0` + strings.Repeat(`}`, 70),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := CanonicalObject([]byte(raw)); e == nil {
				t.Fatal("ambiguous or unbounded approval object accepted")
			}
		})
	}
}
