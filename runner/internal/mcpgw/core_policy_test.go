package mcpgw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Review Focus 5: a core that does not send pin_policy, or sends something unknown, is strict.
func TestCredentialPinPolicyFailsClosed(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"url":"https://u.example/mcp"}`, PinPolicyStrict},
		{`{"url":"https://u.example/mcp","pin_policy":"strict"}`, PinPolicyStrict},
		{`{"url":"https://u.example/mcp","pin_policy":"auto_accept"}`, PinPolicyAutoAccept},
		{`{"url":"https://u.example/mcp","pin_policy":"AUTO_ACCEPT"}`, PinPolicyStrict},
		{`{"url":"https://u.example/mcp","pin_policy":"trust"}`, PinPolicyStrict},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tc.body))
		}))
		c := &HTTPCoreClient{BaseURL: srv.URL, InternalKey: "k"}
		cred, err := c.Token(context.Background(), Proof{AccountID: "a", SessionID: "s"}, "11111111-1111-4111-8111-111111111111")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if cred.PinPolicy != tc.want {
			t.Fatalf("%s: pin_policy = %q, want %q", tc.body, cred.PinPolicy, tc.want)
		}
	}
}
