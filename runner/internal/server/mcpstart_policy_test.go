package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestListConnectionsPinPolicyFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connections":[
			{"id":"c1","name":"a","url":"https://a/mcp","auth_kind":"none","status":"ok","default_tools":{}},
			{"id":"c2","name":"b","url":"https://b/mcp","auth_kind":"none","status":"ok","default_tools":{},"pin_policy":"auto_accept"},
			{"id":"c3","name":"c","url":"https://c/mcp","auth_kind":"none","status":"ok","default_tools":{},"pin_policy":"bogus"}]}`))
	}))
	defer srv.Close()
	c := &HTTPMCPCoreClient{BaseURL: srv.URL, InternalKey: "k"}
	conns, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c1": mcpgw.PinPolicyStrict, "c2": mcpgw.PinPolicyAutoAccept, "c3": mcpgw.PinPolicyStrict}
	for _, cn := range conns {
		if cn.PinPolicy != want[cn.ID] {
			t.Fatalf("%s: pin_policy = %q, want %q", cn.ID, cn.PinPolicy, want[cn.ID])
		}
	}
}
