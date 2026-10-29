package hostgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

// ContextWriter is the custom output contract for Run. WriteContext must return
// promptly when ctx is cancelled, including when another resource blocks a write.
// The runner cancels that context on interruption and joins every copy before
// returning. Write is never called on a ContextWriter. Callers must not access
// its state until Run returns. Plain arbitrary io.Writers are refused before Start.
// bytes.Buffer, strings.Builder, io.Discard and hostgit's capped buffers are
// intrinsically nonblocking; *os.File is inherited directly, without a copier.
// Concurrent stdout/stderr require a concurrency-safe writer unless identical.
type ContextWriter interface {
	io.Writer
	WriteContext(context.Context, []byte) (int, error)
}

var ErrOutputWriter = errors.New("unsupported Git output writer")

const outputWaitDelay = 100 * time.Millisecond

type contextOutput struct {
	ctx    context.Context
	writer ContextWriter
}

func (w *contextOutput) Write(p []byte) (int, error) { return w.writer.WriteContext(w.ctx, p) }

// commandOutput owns a cancellation lifetime separate from request delivery.
// runCommand cancels it on interruption and joins copying before destroying it.
type commandOutput struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newCommandOutput() *commandOutput {
	ctx, cancel := context.WithCancel(context.Background())
	return &commandOutput{ctx: ctx, cancel: cancel}
}

func (o *commandOutput) prepare(cmd *exec.Cmd) error {
	adapt := func(w io.Writer) (io.Writer, error) {
		if w == nil || w == io.Discard {
			return w, nil
		}
		switch w.(type) {
		case *bytes.Buffer, *strings.Builder, *capped, *os.File:
			return w, nil
		}
		if writer, ok := w.(ContextWriter); ok {
			return &contextOutput{ctx: o.ctx, writer: writer}, nil
		}
		return nil, fmt.Errorf("%w: %T must implement ContextWriter", ErrOutputWriter, w)
	}
	// Preserve exec's single copier when stdout and stderr share a writer.
	same := cmd.Stdout != nil && reflect.TypeOf(cmd.Stdout).Comparable() && cmd.Stdout == cmd.Stderr
	stdout, err := adapt(cmd.Stdout)
	if err != nil {
		return err
	}
	stderr := stdout
	if !same {
		stderr, err = adapt(cmd.Stderr)
		if err != nil {
			return err
		}
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// After reap, close inherited output pipes even if an escaped descendant holds
	// them. ContextWriter cancellation handles a blocked Write; WaitDelay alone cannot.
	if cmd.WaitDelay <= 0 || cmd.WaitDelay > outputWaitDelay {
		cmd.WaitDelay = outputWaitDelay
	}
	return nil
}
