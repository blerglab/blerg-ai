package livetls

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// Listen starts only the dedicated route allowlist. A listener failure closes
// the parent server; it must never silently leave a partially working service.
func (i *Identity) Listen(ctx context.Context, addr string, handler http.Handler, onFailure func()) (io.Closer, error) {
	var lc net.ListenConfig
	listener, e := lc.Listen(ctx, "tcp", addr)
	if e != nil {
		return nil, ErrIdentity
	}
	srv := &http.Server{Addr: listener.Addr().String(), Handler: i.Handler(handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(safeTLSLog{}, "", 0)}
	go func() {
		if e := srv.Serve(tls.NewListener(listener, i.ServerTLS())); e != nil && e != http.ErrServerClosed {
			failurelog.Default.Event("live_data_tls_listener_failed", slog.LevelError)
			onFailure()
		}
	}()
	return srv, nil
}

type safeTLSLog struct{}

func (safeTLSLog) Write(p []byte) (int, error) {
	failurelog.Default.Event("live_data_tls_handshake_refused", slog.LevelWarn)
	return len(p), nil
}

// PeerURL is fixed service discovery, never a request-supplied destination.
func (i *Identity) PeerURL() string {
	peer := "runner"
	if i.role == "runner" {
		peer = "core"
	}
	return "https://blerg-" + peer + "-live-data:8443"
}

// Desktop shares the ordinary service DNS but still uses the private TLS port.
func (i *Identity) DesktopPeerURL() string {
	return strings.Replace(i.PeerURL(), "-live-data:", ":", 1)
}
