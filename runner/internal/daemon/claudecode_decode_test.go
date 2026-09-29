package daemon

import (
	"encoding/json"
	"testing"
)

func TestDecodeToolResultContent(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"json string", `"go test -p 1 ./...\nok  \tpkg/a\t0.1s"`, "go test -p 1 ./...\nok  \tpkg/a\t0.1s"},
		{"block array", `[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`, "line one\nline two"},
		{"non-text blocks skipped", `[{"type":"image","source":"x"},{"type":"text","text":"kept"}]`, "kept"},
		{"unknown shape passes through", `{"weird":true}`, `{"weird":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeToolResultContent(json.RawMessage(c.in)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
