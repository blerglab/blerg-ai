package main

import (
	"context"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/api"
)

func TestLiveDataTLSBootstrap(t *testing.T) {
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", "")
	var deps api.Deps
	identity := configureLiveDataTLS(&deps)
	if identity != nil || deps.LiveDataRunnerClient != nil {
		t.Fatal("unconfigured TLS became active")
	}
	root := t.TempDir()
	if e := runLiveDataPKI([]string{"--root", root}); e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", filepath.Join(root, "core"))
	t.Setenv("BLERG_LIVE_DATA_DESKTOP", "false")
	identity = configureLiveDataTLS(&deps)
	if identity == nil || deps.LiveDataRunnerURL != "https://blerg-runner-live-data:8443" || deps.LiveDataRunnerClient == nil || deps.LiveDataRunnerIdentity == nil {
		t.Fatal("dedicated identity not wired")
	}
	runner, e := livetls.Load(filepath.Join(root, "runner"), "runner")
	if e != nil {
		t.Fatal(e)
	}
	listener, e := runner.Listen(context.Background(), "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), func() { t.Error("listener failed") })
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	deps.LiveDataRunnerClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.(*http.Server).Addr)
	}
	res, e := deps.LiveDataRunnerClient.Post(deps.LiveDataRunnerURL+"/internal/live-data/definition", "application/json", nil)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("Core metadata client identity refused")
	}
	if deps.LiveDataAuthorityEpoch != nil || deps.LiveDataSafeRuntime {
		t.Fatal("TLS provisioning enabled polling")
	}
	t.Setenv("BLERG_LIVE_DATA_DESKTOP", "true")
	configureLiveDataTLS(&deps)
	if !deps.LiveDataDesktop || deps.LiveDataRunnerURL != "https://blerg-runner:8443" {
		t.Fatal("desktop discovery incorrect")
	}
	if _, e := os.Stat(filepath.Join(root, "core", "authority")); !os.IsNotExist(e) {
		t.Fatal("CA authority exposed in runtime bundle")
	}
	if e := os.Remove(filepath.Join(root, "core", "key.pem")); e != nil {
		t.Fatal(e)
	}
	if identity = configureLiveDataTLS(&deps); identity != nil {
		t.Fatal("partial bundle stopped public service or opened TLS")
	}
	// The CLI is the same provisioning entrypoint used by both installers.
	if e := runLiveDataPKI([]string{"--unknown"}); e == nil {
		t.Fatal("invalid provisioning flags accepted")
	}
}

func TestLiveDataTLSExpiredStartup(t *testing.T) {
	root := t.TempDir()
	if e := livetls.ProvisionAt(root, time.Now().Add(-100*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_TLS_DIR", filepath.Join(root, "core"))
	var deps api.Deps
	identity := configureLiveDataTLS(&deps)
	if identity != nil || deps.LiveDataRunnerClient != nil {
		t.Fatal("expired optional bundle stopped public startup or authenticated traffic")
	}
}
