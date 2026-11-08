package shellruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// These messages use the fixed helper's private stdio, never browser parameters.
// A fresh challenge prevents a cached legacy probe from advertising persistence.
type persistentProbeRequest struct {
	Version    int                    `json:"version"`
	Challenge  string                 `json:"challenge"`
	Cwd        string                 `json:"cwd"`
	PublicRoot string                 `json:"public_root"`
	Root       protocol.WorkspaceRoot `json:"root"`
}
type persistentProbeResponse struct {
	Version   int                    `json:"version"`
	Challenge string                 `json:"challenge"`
	Root      protocol.WorkspaceRoot `json:"root"`
	Identity  Identity               `json:"identity"`
}

// ProbePersistent verifies the lifetime clock and resolves the account and member
// directory inside the actual installed helper's runtime. The v1 probe is intact.
func ProbePersistent(in io.Reader, out io.Writer) error {
	return probePersistent(in, out, ResolvePersistentIdentity, persistentClock)
}
func probePersistent(in io.Reader, out io.Writer, resolve func(string) (Identity, error), clockFactory func() (ownershell.LeaseClock, error)) error {
	var req persistentProbeRequest
	dec := json.NewDecoder(io.LimitReader(in, 8193))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Version != 2 || req.Challenge == "" || len(req.Challenge) > 256 || !filepath.IsAbs(req.Cwd) || filepath.Clean(req.Cwd) != req.Cwd || !filepath.IsAbs(req.PublicRoot) || filepath.Clean(req.PublicRoot) != req.PublicRoot || req.Root.Inode == 0 {
		return ErrClosed
	}
	rel, err := filepath.Rel(req.PublicRoot, req.Cwd)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrClosed
	}
	clock, err := clockFactory()
	if err != nil || clock == nil {
		return ErrClosed
	}
	if _, elapsed, err := clock(); err != nil || elapsed < 0 {
		return ErrClosed
	}
	actual, err := resolve(req.Cwd)
	if err != nil || actual.Cwd != req.Cwd || actual.MemberRoot.Inode == 0 || !matchesRoot(req.PublicRoot, req.Root) {
		return ErrClosed
	}
	return json.NewEncoder(out).Encode(persistentProbeResponse{Version: 2, Challenge: req.Challenge, Root: req.Root, Identity: actual})
}

func ProbePersistentCommand(ctx context.Context, command Command) (Identity, error) {
	var zero Identity
	challenge, err := newHelperID()
	if err != nil {
		return zero, err
	}
	publicRoot := command.PublicRoot
	if publicRoot == "" {
		publicRoot = command.Identity.Cwd
	}
	req := persistentProbeRequest{Version: 2, Challenge: challenge, Cwd: command.Identity.Cwd, PublicRoot: publicRoot, Root: command.Root}
	data, err := json.Marshal(req)
	if err != nil {
		return zero, ErrClosed
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := append(append([]string(nil), command.Args...), "--probe-persistence")
	cmd := exec.CommandContext(call, command.Executable, args...) //nolint:gosec // trusted fixed runtime command, never browser argv.
	cmd.Env = transportEnvironment()
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = io.Discard
	read, err := cmd.StdoutPipe()
	if err != nil {
		return zero, ErrClosed
	}
	if cmd.Start() != nil {
		return zero, ErrClosed
	}
	output, readErr := io.ReadAll(io.LimitReader(read, 8193))
	// A peer exceeding the bound must not block Wait while filling its stdout.
	if len(output) > 8192 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(output) > 8192 {
		return zero, ErrClosed
	}
	var reply persistentProbeResponse
	dec := json.NewDecoder(bytes.NewReader(output))
	dec.DisallowUnknownFields()
	if dec.Decode(&reply) != nil || dec.Decode(new(any)) != io.EOF || reply.Version != 2 || reply.Challenge != challenge || reply.Root != command.Root || reply.Identity.Cwd != command.Identity.Cwd || reply.Identity.MemberRoot.Inode == 0 {
		return zero, ErrClosed
	}
	return reply.Identity, nil
}
