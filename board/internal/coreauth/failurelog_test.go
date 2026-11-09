package coreauth

import (
	"bytes"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

func TestRefreshLogsExcludeRemoteContentAndURLs(t *testing.T) {
	var out bytes.Buffer
	old, writer := failurelog.Default, log.Writer()
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	log.SetOutput(&out)
	defer func() { failurelog.Default = old; log.SetOutput(writer) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"CANARY_SECRET":"invalid-public-key"}`))
	}))
	defer server.Close()
	NewClient(server.URL).refreshJWKS(t.Context())
	bad := NewClient("http://[CANARY_SECRET")
	bad.refreshJWKS(t.Context())
	bad.refreshRevocations(t.Context())
	if strings.Contains(out.String(), "CANARY_SECRET") || !strings.Contains(out.String(), "JWKS invalid key") || !strings.Contains(out.String(), "JWKS fetch") || !strings.Contains(out.String(), "revocations fetch") {
		t.Fatal(out.String())
	}
}
