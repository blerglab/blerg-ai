package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

// OwnerShellRuntime exposes P5's prepared-launch registration and P0b lifecycle
// to the daemon/pod workspace coordinator. Unregistered/scalar launches refuse.
func (c *WSClient) OwnerShellRuntime() *shellruntime.Service {
	return c.OwnerShellRuntimeContext(context.Background())
}
func (c *WSClient) OwnerShellRuntimeContext(ctx context.Context) *shellruntime.Service {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shellRuntime == nil {
		c.shellRuntime = shellruntime.NewService(func(frame shellruntime.RPC) error { return c.SendContext(context.WithoutCancel(ctx), frame) })
		c.shellRuntime.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
		c.handlers["owner_shell_request"] = func(raw []byte) { c.shellRuntime.HandleContext(context.WithoutCancel(ctx), raw) }
	}
	return c.shellRuntime
}
func (c *WSClient) sendFair(parent context.Context, raw []byte) error {
	c.mu.Lock()
	conn := c.conn
	options, scoped := parent.Value(fairWriteKey{}).(fairWriteOptions)
	if conn == nil || (scoped && conn != options.connection) {
		c.mu.Unlock()
		return errors.New("daemon: disconnected")
	}
	if c.fair == nil || c.fairConn != conn {
		if c.fair != nil {
			c.fair.Close()
		}
		c.fairConn = conn
		c.fair = shellruntime.NewFairWriter(func(ctx context.Context, data []byte) error {
			timeout, wait := time.Second, time.Second
			scope, file := ctx.Value(fairWriteKey{}).(fairWriteOptions)
			if file {
				timeout, wait = scope.timeout, scope.wait
			}
			lockUntil := time.Time{}
			if file {
				lockUntil = scope.startBy
			}
			if end, ok := ctx.Deadline(); ok && (lockUntil.IsZero() || end.Before(lockUntil)) {
				lockUntil = end
			}
			if ctx.Err() != nil || (file && !time.Now().Before(scope.startBy)) {
				return errors.New("daemon queued write expired")
			}
			if !c.lockWriterWithin(ctx, wait, lockUntil) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("daemon writer busy")
			}
			defer c.writeMu.Unlock()
			if ctx.Err() != nil || (file && !time.Now().Before(scope.startBy)) {
				return errors.New("daemon queued write expired")
			}
			if c.currentConn() != conn {
				return errors.New("daemon connection replaced")
			}
			deadline := time.Now().Add(timeout)
			if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
				deadline = end
			}
			if file && !scope.notAfter.IsZero() && scope.notAfter.Before(deadline) {
				deadline = scope.notAfter
			}

			if err := conn.SetWriteDeadline(deadline); err != nil {
				return err
			}
			// Only the writer owner may interrupt a frame. Join the callback before
			// clearing deadlines or allowing unrelated writes onto this connection.
			finished, interrupted := make(chan struct{}), make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(interrupted)
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
		})
	}
	writer := c.fair
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if scoped {
		return writer.SendOrdinary(ctx, raw)
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
