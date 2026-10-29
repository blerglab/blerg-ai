//go:build !linux

package shellruntime

import "context"
import "github.com/blerglab/blerg-ai/runner/internal/protocol"

// TasksPerShell bounds one shell's processes and threads. Builds use about one thread per core, so it
// leaves room for real parallel work; a runaway fork still ends the shell, with the reason shown.
const TasksPerShell = 1024
const SessionTaskBudget = 4 * TasksPerShell

// Unsupported containment fails closed; a process group alone cannot contain detached jobs.
type tracedProcess struct {
	output chan []byte
	done   chan struct{}
}

func startTraced(Identity, protocol.WorkspaceRoot, string) (*tracedProcess, error) {
	return nil, ErrClosed
}
func (*tracedProcess) stop(context.Context) error  { return ErrClosed }
func (*tracedProcess) input([]byte) error          { return ErrClosed }
func (*tracedProcess) resize(uint16, uint16) error { return ErrClosed }

func matchesRoot(string, protocol.WorkspaceRoot) bool { return false }

func (*tracedProcess) freeze(context.Context) error { return ErrClosed }
func (*tracedProcess) thaw(context.Context) error   { return ErrClosed }
