package shellruntime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const FixedContainerSupervisor = "/usr/local/bin/blerg-shell-supervisor"

// Command is assembled by trusted daemon/runtime state only. No browser field
// supplies an executable, argv, container, account or cwd.
type Command struct {
	OwnedStarted        func(int) error // trusted durable helper identity before bootstrap
	identityInvalidated func()
	PublicRoot          string
	Recovery            func(context.Context, protocol.SupervisorLaunch) error
	Root                protocol.WorkspaceRoot
	Consent             *protocol.HostShellConsent
	Policy              protocol.ShellPolicy
	Executable          string
	Args                []string
	Identity            Identity
}

func HostCommand(executable string, identity Identity) Command {
	return Command{Executable: executable, Args: []string{"shell-supervisor"}, Identity: identity}
}
func SandboxCommand(container, account string, identity Identity) Command {
	return Command{Executable: "docker", Args: []string{"exec", "-i", "--user", account, container, FixedContainerSupervisor}, Identity: identity}
}

type helperPeer struct {
	expectedClose       atomic.Bool
	closeMu             sync.Mutex
	identityInvalidated func()
	clean               bool
	cmd                 *exec.Cmd
	input               *os.File
	output              chan []byte
	done                chan struct{}
	forwardDone         chan struct{}
	writeMu             sync.Mutex
	mu                  sync.Mutex
	pending             map[uint64]chan bool
	seq                 atomic.Uint64
}

func startPeer(ctx context.Context, command Command, g ownershell.SignedGrant, key ed25519.PublicKey) (*helperPeer, error) {
	cmd := exec.Command(command.Executable, command.Args...) //nolint:gosec,noctx // fixed trusted runtime command; local grant/containment supervisor owns lifetime
	cmd.Env = transportEnvironment()
	stdin, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	read, stdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = write.Close()
		return nil, err
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	done := make(chan struct{})
	if err = startOwnedCommand(cmd, done); err != nil {
		_ = stdin.Close()
		_ = write.Close()
		_ = read.Close()
		_ = stdout.Close()
		return nil, err
	}
	_ = stdin.Close()
	_ = stdout.Close()
	if command.OwnedStarted != nil {
		if err := command.OwnedStarted(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			close(done)
			_ = write.Close()
			_ = read.Close()
			return nil, err
		}
	}
	p := &helperPeer{identityInvalidated: command.identityInvalidated, cmd: cmd, input: write, output: make(chan []byte, 8), done: done, pending: make(map[uint64]chan bool)}
	go p.read(read)
	if err = p.call(ctx, HelperFrame{Op: "bootstrap", Grant: g, Expected: g.Grant, Key: key, Root: command.Root, PublicRoot: command.PublicRoot, Identity: command.Identity}); err != nil {
		_ = write.Close()
		return p, err
	}
	return p, nil
}
func (p *helperPeer) read(in *os.File) {
	defer close(p.done)
	defer close(p.output)
	defer func() { _ = in.Close() }()
	defer func() {
		_ = p.input.Close()
		if p.identityInvalidated != nil && !p.expectedClose.Load() {
			p.identityInvalidated()
		}
		err := p.cmd.Wait()
		if p.identityInvalidated != nil && (!p.expectedClose.Load() || err != nil) {
			p.identityInvalidated()
		}
		p.mu.Lock()
		p.clean = err == nil || (p.cmd.ProcessState != nil && p.cmd.ProcessState.ExitCode() == 3)
		p.mu.Unlock()
	}()
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 65536)
	for scan.Scan() {
		var f HelperFrame
		if json.Unmarshal(scan.Bytes(), &f) != nil {
			return
		}
		if f.Op == "identity_changed" {
			if p.identityInvalidated != nil {
				p.identityInvalidated()
			}
			continue
		}
		if f.Op == "output" {
			if len(f.Data) > 16384 {
				return
			}
			select {
			case p.output <- f.Data:
			default:
				return
			}
			continue
		}
		p.mu.Lock()
		reply := p.pending[f.ID]
		delete(p.pending, f.ID)
		p.mu.Unlock()
		if reply != nil {
			reply <- f.OK
		}
	}
}
func (p *helperPeer) call(ctx context.Context, f HelperFrame) error {
	emptyTreeOK := f.Op == "close" || f.Op == "freeze" || f.Op == "thaw"
	if f.Op == "close" {
		p.expectedClose.Store(true)
	}
	if emptyTreeOK {
		select {
		case <-p.done:
			p.mu.Lock()
			clean := p.clean
			p.mu.Unlock()
			if clean {
				return nil
			}
			return ErrClosed
		default:
		}
	}
	f.ID = p.seq.Add(1)
	reply := make(chan bool, 1)
	p.mu.Lock()
	p.pending[f.ID] = reply
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, f.ID); p.mu.Unlock() }()
	data, err := json.Marshal(f)
	if err != nil {
		return ErrClosed
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = p.input.SetWriteDeadline(deadline)
	_, err = p.input.Write(data)
	p.writeMu.Unlock()
	if err != nil {
		if emptyTreeOK {
			select {
			case <-p.done:
				p.mu.Lock()
				clean := p.clean
				p.mu.Unlock()
				if clean {
					return nil
				}
			case <-ctx.Done():
			}
		}
		return ErrClosed
	}
	select {
	case ok := <-reply:
		if ok {
			return nil
		}
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		select {
		case ok := <-reply:
			if ok {
				return nil
			}
		default:
		}
		if emptyTreeOK {
			p.mu.Lock()
			clean := p.clean
			p.mu.Unlock()
			if clean {
				return nil
			}
		}
		return ErrClosed
	}
}
