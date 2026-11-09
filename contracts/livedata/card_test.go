package livedata

import (
	"encoding/json"
	"testing"
)

func TestNativeMCPCardSetup(t *testing.T) {
	for _, kind := range []string{"stat", "gauge"} {
		if !(&CardSpec{Kind: kind}).Native() {
			t.Fatal("native scalar refused", kind)
		}
	}
	for _, card := range []*CardSpec{nil, {}, {Kind: "text"}, {Kind: "chart"}, {Kind: "stat", Sparkline: true}, {Kind: "gauge", Spec: json.RawMessage(`{"mark":"line"}`)}} {
		if card.Native() {
			t.Fatal("unsupported setup accepted")
		}
	}
}
