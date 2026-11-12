package mcpgw

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func TestLiveProviderReleaseOnlyExactAttemptAndScope(t *testing.T) {
	scope := ld.Scope{Binding: "fixture-binding", Owner: "fixture-owner"}
	provider := &LiveProvider{pending: map[string]liveExchange{"first": {scope: scope}, "second": {scope: scope}}}
	ctx, cancel := context.WithCancel(ld.WithProviderRequest(t.Context(), ld.ProviderRequest{Attempt: "first", Stage: "list"}))
	cancel()
	provider.Release(context.Background(), scope)
	wrong := scope
	wrong.Owner = "another-owner"
	provider.Release(ctx, wrong)
	if len(provider.pending) != 2 {
		t.Fatal("unrelated context discarded an exchange")
	}
	provider.Release(ctx, scope)
	if len(provider.pending) != 1 {
		t.Fatal("canceled attempt kept its credential exchange")
	}
	if _, ok := provider.pending["second"]; !ok {
		t.Fatal("another attempt was discarded")
	}
	provider.Release(ctx, scope)
	if len(provider.pending) != 1 {
		t.Fatal("release is not idempotent")
	}
}

func TestLiveProviderRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"90", 90 * time.Second}, {now.Add(2 * time.Hour).Format(http.TimeFormat), 2 * time.Hour},
		{"garbage", 0}, {"-1", 0}, {now.Add(-time.Hour).Format(http.TimeFormat), 0},
		{"999999999999", 30 * 24 * time.Hour}, {strings.Repeat("9", 129), 0},
	} {
		if got := liveRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("retry delay=%s want=%s", got, tc.want)
		}
	}
}

type liveResponseTransport struct{ body io.ReadCloser }

func (r liveResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: r.body}, nil
}

type liveCountingBody struct{ n int }

func (b *liveCountingBody) Read(p []byte) (int, error) {
	if b.n >= 6*1024*1024 {
		return 0, io.EOF
	}
	p = p[:min(len(p), 6*1024*1024-b.n)]
	for i := range p {
		p[i] = 'x'
	}
	b.n += len(p)
	return len(p), nil
}
func (*liveCountingBody) Close() error { return nil }
func TestLiveProviderBoundsWireResponseBeforeParsing(t *testing.T) {
	body := &liveCountingBody{}
	state := &grantState{liveOnly: true, g: &Gateway{client: &http.Client{Transport: liveResponseTransport{body: body}}}}
	_, _, _, err := state.post(t.Context(), Credential{URL: "https://provider.invalid/mcp"}, []byte("{}"), "1", "", "")
	if !errors.Is(err, errLiveResponseLimit) || body.n > 5*1024*1024+1 {
		t.Fatal("live response was unbounded before parsing")
	}
}
