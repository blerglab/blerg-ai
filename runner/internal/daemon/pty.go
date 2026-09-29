package daemon

import (
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/creack/pty"
)

// Session represents a single running PTY session.
type Session struct {
	ID          string
	ProjectPath string
	Repo        string
	ptmx        *os.File
	cmd         *exec.Cmd
	seq         atomic.Int64
	OutputCh    chan []byte   // raw PTY output chunks
	done        chan struct{} // closed when session ends
	exitCode    int
	signal      *string
}

// winsize converts a client-supplied terminal size to the uint16 pair the
// kernel takes, clamping instead of wrapping (a 65616-column request must not
// become 80).
func winsize(cols, rows int) *pty.Winsize {
	clamp := func(n int) uint16 {
		if n < 0 {
			return 0
		}
		if n > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(n)
	}
	return &pty.Winsize{Cols: clamp(cols), Rows: clamp(rows)}
}

// NewSession starts a new PTY session running the given command.
// If command is empty, it defaults to ["claude", "--dangerously-skip-permissions"].
func NewSession(id, projectPath string, cols, rows int, env []string, command []string) (*Session, error) {
	if len(command) == 0 {
		claudePath, err := exec.LookPath("claude")
		if err != nil {
			claudePath = "claude"
		}
		command = []string{claudePath, "--dangerously-skip-permissions"}
	}

	cmd := exec.Command(command[0], command[1:]...) //nolint:gosec,noctx // argv is assembled by the daemon (engine binary or tmux attach), never through a shell; noctx: a long-lived interactive PTY session; its lifetime is managed by Kill, not a context
	cmd.Dir = projectPath
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, winsize(cols, rows))
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:          id,
		ProjectPath: projectPath,
		ptmx:        ptmx,
		cmd:         cmd,
		OutputCh:    make(chan []byte, 256),
		done:        make(chan struct{}),
	}

	go s.readLoop()

	return s, nil
}

// readLoop reads from the PTY master until EOF, forwarding chunks to OutputCh.
func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.OutputCh <- chunk
		}
		if err != nil {
			// EOF or closed — session ended
			break
		}
	}
	// Collect exit status BEFORE closing OutputCh: streamSession reads
	// exitCode once the channel is closed, and the close is what orders that
	// read after this write (it used to be the other way round — a data race).
	if err := s.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.exitCode = exitErr.ExitCode()
		}
	}
	close(s.OutputCh)

	close(s.done)
}

// Write sends data to the PTY (i.e., to the process stdin).
func (s *Session) Write(data []byte) error {
	_, err := s.ptmx.Write(data)
	return err
}

// Resize resizes the PTY window.
func (s *Session) Resize(cols, rows int) error {
	return pty.Setsize(s.ptmx, winsize(cols, rows))
}

// Kill terminates the session process.
func (s *Session) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// NextSeq atomically increments and returns the output sequence counter.
func (s *Session) NextSeq() int64 {
	return s.seq.Add(1)
}

// Done returns a channel that is closed when the session has ended.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// ensure io.EOF is not flagged as unused
var _ = io.EOF
