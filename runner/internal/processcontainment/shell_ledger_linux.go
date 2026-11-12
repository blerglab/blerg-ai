//go:build linux

package processcontainment

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// ShellLedger receives the tracer's before-execution pidfds on a dedicated
// inherited socket. Wait is called only after the helper has been reaped.
// A truncated stream or missing ledger can never manufacture an empty proof.
type ShellLedger struct {
	file   *os.File
	ledger pidLedger
	done   chan struct{}
	err    error
}

func NewShellLedger() (*ShellLedger, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	l := &ShellLedger{file: os.NewFile(uintptr(fds[0]), "shell-ledger"), done: make(chan struct{})}
	child := os.NewFile(uintptr(fds[1]), "shell-ledger-child")
	raw, err := l.file.SyscallConn()
	if err != nil {
		_ = l.file.Close()
		_ = child.Close()
		return nil, nil, err
	}
	go func() {
		defer close(l.done)
		_, l.err = io.Copy(io.Discard, &streamReader{raw: raw, ledger: &l.ledger, oob: make([]byte, unix.CmsgSpace(16*4))})
	}()
	return l, child, nil
}

func (l *ShellLedger) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.done:
	}
	l.ledger.mu.Lock()
	defer l.ledger.mu.Unlock()
	if l.err != nil || l.ledger.broken != "" || l.ledger.records == 0 {
		return ErrUnproven
	}
	for len(l.ledger.fds) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		l.ledger.fds, err = exited(l.ledger.fds, 100, true)
		if err != nil {
			return err
		}
	}
	return nil
}
func (l *ShellLedger) Close() {
	_ = l.file.Close()
	<-l.done
	l.ledger.close()
}

// RecordShellProcess blocks the tracer until its pidfd has been transferred.
// Failure stops the tree before the unrecorded process can execute user code.
func RecordShellProcess(file *os.File, pidfd int) error {
	for {
		err := unix.Sendmsg(int(file.Fd()), []byte{'\n'}, unix.UnixRights(pidfd), nil, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
