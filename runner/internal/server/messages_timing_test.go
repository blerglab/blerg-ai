package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestMessageAnswerHoldDefaultsAndQueryClamp(t *testing.T) {
	for _, tc := range []struct {
		name, query      string
		configured, want time.Duration
	}{
		{name: "default", want: 25 * time.Second},
		{name: "shorter requested hold", query: "?wait=1", want: time.Second},
		{name: "request cannot extend cap", query: "?wait=60", want: 25 * time.Second},
		{name: "zero uses default", query: "?wait=0", want: 25 * time.Second},
		{name: "negative uses default", query: "?wait=-1", want: 25 * time.Second},
		{name: "malformed uses default", query: "?wait=bad", want: 25 * time.Second},
		{name: "instance timing override", configured: 40 * time.Millisecond, want: 40 * time.Millisecond},
		{name: "query cannot extend instance cap", query: "?wait=1", configured: 40 * time.Millisecond, want: 40 * time.Millisecond},
		{name: "negative override uses default", configured: -time.Second, want: 25 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &API{answerHold: tc.configured}
			if got := api.messageAnswerHold(httptest.NewRequest("GET", "/answer"+tc.query, nil)); got != tc.want {
				t.Fatalf("hold=%v, want %v", got, tc.want)
			}
		})
	}
	// A test's override must not affect another API in the same process.
	first, second := &API{answerHold: time.Millisecond}, &API{}
	req := httptest.NewRequest("GET", "/answer", nil)
	if first.messageAnswerHold(req) == second.messageAnswerHold(req) {
		t.Fatal("timing override is not scoped to its API")
	}
}
