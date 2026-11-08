package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

var hostKeeperFactoryMu sync.RWMutex
var hostKeeperFactory func() shellruntime.HostBridge

// SetHostKeeperFactory is installed by the Linux startup fence, not RPC.
func SetHostKeeperFactory(factory func() shellruntime.HostBridge) {
	hostKeeperFactoryMu.Lock()
	hostKeeperFactory = factory
	hostKeeperFactoryMu.Unlock()
}
func newHostKeeperBridge() shellruntime.HostBridge {
	hostKeeperFactoryMu.RLock()
	factory := hostKeeperFactory
	hostKeeperFactoryMu.RUnlock()
	if factory != nil {
		return factory()
	}
	return nil
}

// OwnerShellRuntime exposes P5's prepared-launch registration and P0b lifecycle
// to the daemon/pod workspace coordinator. Unregistered/scalar launches refuse.
func (c *WSClient) OwnerShellRuntime() *shellruntime.Service {
	return c.OwnerShellRuntimeContext(context.Background())
}
func (c *WSClient) OwnerShellRuntimeContext(ctx context.Context) *shellruntime.Service {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shellRuntime == nil {
		c.shellRuntime = shellruntime.NewService(func(frame shellruntime.RPC) error {
			if frame.Type == "daemon_shell_hello" {
				c.refreshKeeperCapabilities(ctx)
			}
			return c.SendContext(context.WithoutCancel(ctx), frame)
		})
		if bridge := newHostKeeperBridge(); bridge != nil {
			if err := c.shellRuntime.ConfigureHostRetirements(filepath.Join(DaemonStateDir(), "shell-retirements")); err != nil {
				logDaemonFailure("host shell retirement journal unavailable", err)
			}
			c.shellRuntime.SetHostBridge(bridge)
		}
		// Shell output carries the shell's lifetime up to the moment its frame
		// starts writing: a shell that ends drops a frame still queued or waiting
		// for the writer. See shellOutputKey for a frame already being written.
		c.shellRuntime.SetOutputSender(c.sendShellOutput)
		c.shellRuntime.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
		c.handlers["owner_shell_request"] = func(raw []byte) { c.shellRuntime.HandleContext(context.WithoutCancel(ctx), raw) }
		c.handlers["daemon_shell_welcome"] = func(raw []byte) {
			var welcome struct {
				Hello protocol.ShellLinkHello    `json:"shell_hello"`
				Link  *protocol.ShellLinkWelcome `json:"shell_link"`
			}
			if json.Unmarshal(raw, &welcome) != nil {
				logDaemonFailure("host shell welcome refused", errors.New("invalid shell welcome shape"))
				return
			}
			go func() {
				call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				defer cancel()
				if err := c.shellRuntime.AcceptShellLink(call, welcome.Hello, welcome.Link); err != nil {
					logDaemonFailure("host shells waiting for keeper authority", err)
				}
			}()
		}
	}
	return c.shellRuntime
}

// Re-probing runs on the shell recovery worker, never the agent read loop.
// A keeper missing at initial hello must not permanently erase persistence
// capability after it returns. Preserve the raw server offer for negotiation.
func (c *WSClient) refreshKeeperCapabilities(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, keeperProbeContext{}, true)
	runtime := c.config.WorkspaceCapabilities
	if c.config.WorkspaceCapabilitiesProvider != nil {
		runtime = c.config.WorkspaceCapabilitiesProvider(ctx)
	}
	server, _ := c.shellServerCaps.Load().(protocol.WorkspaceCapabilities)
	c.shellRuntime.SetCapabilities(server, runtime)
}

// shellOutputKey marks shell output. The connection is shared by every
// session, so once a shell output frame has started writing it finishes under
// the writer's own bounded deadline: the shell's cancellation never shortens
// that deadline or interrupts the frame, which would cut a WebSocket message
// in half and close the connection for every session on it. Cancellation
// still applies while the frame is queued or waiting for the writer.
type shellOutputKey struct{}

// sendShellOutput sends one shell output frame with its shell's lifetime,
// marked so a frame that has started writing is never interrupted by it.
func (c *WSClient) sendShellOutput(life context.Context, frame shellruntime.RPC) error {
	return c.SendContext(context.WithValue(life, shellOutputKey{}, true), frame)
}

// serverDialer is the WebSocket dialer for the server connection; tests may
// substitute one with an instrumented net.Conn.
var serverDialer = websocket.DefaultDialer

// frameWriter is the only code that writes to conn after its hello. FairWriter
// runs it on one goroutine, so a frame's deadline starts when the frame does.
func (c *WSClient) frameWriter(conn *websocket.Conn) func(context.Context, []byte) error {
	return func(ctx context.Context, data []byte) error {
		timeout := time.Second
		scope, file := ctx.Value(fairWriteKey{}).(fairWriteOptions)
		if file {
			timeout = scope.timeout
		}
		if ctx.Err() != nil {
			return errors.New("daemon queued write expired")
		}
		if c.currentConn() != conn {
			return errors.New("daemon connection replaced")
		}
		// From here the frame is written. Shell output is bounded only by the
		// writer's own timeout and is never interrupted by its shell's end. A
		// chunk is small and outlives its reply's deadline: the reply stops at
		// the next chunk, and cutting this one off would cost the whole link.
		committed := ctx.Value(shellOutputKey{}) != nil
		if committed {
			ctx = context.WithoutCancel(ctx)
		}
		chunk := file && scope.chunked
		deadline := time.Now().Add(timeout)
		if !chunk {
			if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
				deadline = end
			}
			if file && !scope.notAfter.IsZero() && scope.notAfter.Before(deadline) {
				deadline = scope.notAfter
			}
		}

		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		// Only the writer owner may interrupt a frame. Join the callback before
		// clearing deadlines or allowing unrelated writes onto this connection.
		finished, interrupted := make(chan struct{}), make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(interrupted)
			if chunk && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return
			}
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				_ = conn.NetConn().SetWriteDeadline(time.Now())
				select {
				case <-finished:
					return
				case <-ticker.C:
				}
			}
		})
		defer func() {
			close(finished)
			if !stop() {
				<-interrupted
			}
			_ = conn.NetConn().SetWriteDeadline(time.Time{})
			_ = conn.SetWriteDeadline(time.Time{})
		}()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := conn.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			_ = conn.Close()
		}
		return err
	}
}

// writerFor returns the current connection's writer, created on first use.
func (c *WSClient) writerFor(bound *websocket.Conn) (*shellruntime.FairWriter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn := c.conn
	if conn == nil || (bound != nil && conn != bound) {
		return nil, errors.New("daemon: disconnected")
	}
	if c.fair == nil || c.fairConn != conn {
		if c.fair != nil {
			c.fair.Close()
		}
		c.fairConn = conn
		c.fair = shellruntime.NewFairWriter(c.frameWriter(conn))
	}
	return c.fair, nil
}

func (c *WSClient) sendFair(parent context.Context, raw []byte) error {
	options, scoped := parent.Value(fairWriteKey{}).(fairWriteOptions)
	writer, err := c.writerFor(options.connection)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if scoped {
		return writer.SendOrdinary(ctx, raw, options.startBy)
	}
	return writer.Send(ctx, raw)
}
func isOwnerShellFrame(raw []byte) bool {
	var frame struct{ Type string }
	return json.Unmarshal(raw, &frame) == nil && (frame.Type == "owner_shell_output" || frame.Type == "owner_shell_result" || frame.Type == "owner_shell_phase")
}

// SandboxShellCommand pins the same Docker context as the session's agent.
// A later desktop context switch must never redirect shell control elsewhere.
func SandboxShellCommand(container, account string, identity shellruntime.Identity) shellruntime.Command {
	command := shellruntime.SandboxCommand(container, account, identity)
	command.Args = pinnedDockerArgs(container, command.Args...)
	return command
}
