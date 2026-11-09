package main

import (
	"strings"
	"testing"
)

func TestReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		ok           bool
	}{
		{"pass", `{"Action":"pass","Test":"TestRequired"}`, true},
		{"empty", "", false},
		{"wrong", `{"Action":"pass","Test":"TestOther"}`, false},
		{"skip", "{\"Action\":\"skip\",\"Test\":\"TestRequired/sub\"}\n{\"Action\":\"pass\",\"Test\":\"TestRequired\"}", false},
		{"fail", `{"Action":"fail","Test":"TestRequired"}`, false},
		{"malformed", "not json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := receipts(strings.NewReader(tc.events), "TestRequired"); (err == nil) != tc.ok {
				t.Fatalf("error = %v, want success %v", err, tc.ok)
			}
		})
	}
}
