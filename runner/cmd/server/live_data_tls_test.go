package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/runner/internal/server"
)

func TestLiveDataTLSBootstrap(t *testing.T) {
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", "")
	api := &server.API{}
	if identity := configureLiveDataTLS(api); identity != nil {
		t.Fatal("unconfigured TLS became active")
	}
	root := t.TempDir()
	if e := livetls.Provision(root); e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", filepath.Join(root, "runner"))
	identity := configureLiveDataTLS(api)
	if identity == nil {
		t.Fatal("runner identity not wired")
	}
	api.SetCoreCredentials("", "fixture-component")
	mux := http.NewServeMux()
	api.RegisterMCPRoutes(mux)
	listener, e := identity.Listen(context.Background(), "127.0.0.1:0", mux, func() { t.Error("listener failed") })
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	core, e := livetls.Load(filepath.Join(root, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	client := core.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.(*http.Server).Addr)
	}
	req, _ := http.NewRequest("POST", "https://blerg-runner/internal/live-data/definition", strings.NewReader("invalid json"))
	req.Header.Set("X-Internal-Key", "fixture-component")
	res, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("wired metadata identity refused: %d", res.StatusCode)
	}
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", filepath.Join(root, "core"))
	if identity := configureLiveDataTLS(api); identity != nil {
		t.Fatal("invalid bundle stopped public service or opened TLS")
	}
}

func TestLiveDataTLSExpiredStartup(t *testing.T) {
	root := t.TempDir()
	if e := livetls.ProvisionAt(root, time.Now().Add(-100*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", filepath.Join(root, "runner"))
	api := &server.API{}
	identity := configureLiveDataTLS(api)
	if identity != nil {
		t.Fatal("expired optional bundle stopped public startup or opened TLS")
	}
}
