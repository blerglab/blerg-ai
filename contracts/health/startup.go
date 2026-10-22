package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// SustainedFailures is how many initialization attempts in a row must fail before /readyz says
// why (about 15 s of backoff): a database that is only restarting stays a plain "not ready".
const SustainedFailures = 5

// Gate serves liveness immediately, and refuses traffic until initialization completes.
// Its zero value is ready for use.
type Gate struct {
	handler  atomic.Pointer[http.Handler]
	draining atomic.Bool

	mu       sync.Mutex
	failures int
	reason   string
}

// reportFailure records the class of a failed initialization attempt (ReadinessClass: never a
// user, host, port, database name or message text, since /readyz is reachable from outside).
func (g *Gate) reportFailure(failures int, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures, g.reason = failures, reason
}

// notReady is the body of a 503 before initialization: after a sustained failure it says why.
func (g *Gate) notReady() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures >= SustainedFailures && g.reason != "" {
		return fmt.Sprintf("not ready: database initialization failed %d times in a row; last: %s", g.failures, g.reason)
	}
	return "not ready"
}

func (g *Gate) SetHandler(handler http.Handler) { g.handler.Store(&handler) }

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/livez" {
		_, _ = w.Write([]byte("ok"))
		return
	}
	if g.draining.Load() && r.URL.Path == "/readyz" {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	if next := g.handler.Load(); next != nil {
		(*next).ServeHTTP(w, r)
		return
	}
	body := "not ready"
	if r.URL.Path == "/readyz" {
		body = g.notReady()
	}
	http.Error(w, body, http.StatusServiceUnavailable)
}

// Server keeps the same listener through database initialization and normal operation.
type Server struct {
	Gate
	server *http.Server
	done   chan error
}

// Listen binds before initialization so probes see 503 readiness and 200 liveness
// throughout a database outage, rather than triggering a restart loop.
func Listen(ctx context.Context, addr string) (*Server, error) {
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &Server{done: make(chan error, 1)}
	srv.server = &http.Server{Addr: listener.Addr().String(), Handler: &srv.Gate, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() { srv.done <- srv.server.Serve(listener) }()
	return srv, nil
}

func (s *Server) Close() error { return s.server.Close() }

// Wait serves until the server fails. A server that keeps state only in its own memory (the
// runner) uses it and exits at once on SIGTERM; see ServeUntilSignal for the others.
func (s *Server) Wait() error { return <-s.done }

// DrainDelay is how long a terminating server keeps serving after SIGTERM, with /readyz
// answering 503, before it stops accepting connections: about the time Kubernetes takes to take
// the pod out of the Service's endpoints and the ingress's upstreams. It does what a preStop sleep
// would, in images (distroless) that have no sleep binary and on clusters without the preStop
// sleep action.
const DrainDelay = 5 * time.Second

// ShutdownGrace bounds how long in-flight requests may take to finish after the drain. Together
// with DrainDelay it stays under Kubernetes' default 30 s terminationGracePeriodSeconds.
const ShutdownGrace = 20 * time.Second

// ServeUntilSignal serves until the server fails or SIGTERM (or an interrupt) arrives. On a
// signal it drains for DrainDelay, then shuts down gracefully (Server.Shutdown: no new
// connections, in-flight requests finish, up to ShutdownGrace) and returns nil. For stateless
// services behind a rolling update (core, the board); never for the runner, which must exit at
// once (see runner-deployment.yaml).
func (s *Server) ServeUntilSignal(ctx context.Context, logf func(string, ...any)) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(stop)
	return s.serveUntil(ctx, stop, DrainDelay, ShutdownGrace, logf)
}

func (s *Server) serveUntil(ctx context.Context, stop <-chan os.Signal, delay, grace time.Duration, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	select {
	case err := <-s.done:
		return err
	case sig := <-stop:
		logf("%s received: draining for %s, then shutting down", sig, delay)
	}
	s.draining.Store(true)
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
	case err := <-s.done:
		timer.Stop()
		return err
	}
	// The grace period is its own: a cancelled caller must not cut in-flight requests short.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	err := s.server.Shutdown(shutdownCtx)
	if served := <-s.done; served != nil && !errors.Is(served, http.ErrServerClosed) && err == nil {
		err = served
	}
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logf("shut down")
	return nil
}

// Retry configures RetryDatabase.
type Retry struct {
	// Logf logs each attempt with its sanitized reason.
	Logf func(string, ...any)
	// Gate, if set, is told each failure's class (ReadinessClass) so its /readyz says what kind of
	// failure it is after SustainedFailures; the full sanitized reason goes to the log only.
	Gate *Gate
	// Secrets are literal values (the DSN, its password) removed from every reason.
	Secrets []string
}

// RetryDatabase retries database connection and migration initialization until success
// or cancellation. Each failure is logged with a reason from SafeReason, which removes
// credentials and connection strings.
func RetryDatabase(ctx context.Context, attempt func(context.Context) error, retry Retry) error {
	return retryDatabase(ctx, attempt, pause, retry)
}

func retryDatabase(ctx context.Context, attempt func(context.Context) error, wait func(context.Context, time.Duration) error, retry Retry) error {
	logf := retry.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	delay := time.Second
	for number := 1; ; number++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := attempt(ctx)
		if err == nil {
			logf("database initialization attempt %d succeeded; migrations applied", number)
			return nil
		}
		reason := SafeReason(err, retry.Secrets...)
		if retry.Gate != nil {
			retry.Gate.reportFailure(number, ReadinessClass(err))
		}
		logf("database initialization attempt %d failed (%s); retrying in %s", number, reason, delay)
		if err := wait(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
