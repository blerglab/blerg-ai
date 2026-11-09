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
		// for the writer. A started frame finishes under the socket write bound.
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

// sendShellOutput sends output with its shell lifetime for queue admission.
// Once started, all frames finish under the independent socket write bound.
func (c *WSClient) sendShellOutput(life context.Context, frame shellruntime.RPC) error {
	return c.SendContext(life, frame)
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
		// Once a frame starts, only the connection's write bound may interrupt it.
		// Request cancellation still fences admission and the next frame, but
		// interrupting this frame would poison the socket for every session.
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			logDaemonSocketFailure("socket write deadline", err)
			return err
		}
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
		err := conn.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			logDaemonSocketFailure("socket frame write closed connection", err)
			closeDaemonSocket(conn, "frame_write", err)
		} else {
			err = ctx.Err()
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
