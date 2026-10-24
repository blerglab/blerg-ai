package shellruntime

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const ProbeVersion = "owner-shell-v1"

func Probe(out io.Writer) error {
	if runtime.GOOS != "linux" {
		return ErrClosed
	}
	_, err := io.WriteString(out, ProbeVersion+"\n")
	return err
}
func ProbeCommand(ctx context.Context, command Command) error {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := append(append([]string(nil), command.Args...), "--probe")
	cmd := exec.CommandContext(call, command.Executable, args...) //nolint:gosec // fixed trusted daemon/pod/container executable, no browser argv
	cmd.Env = transportEnvironment()
	cmd.Stderr = io.Discard
	read, err := cmd.StdoutPipe()
	if err != nil {
		return ErrClosed
	}
	if cmd.Start() != nil {
		return ErrClosed
	}
	out, err := io.ReadAll(io.LimitReader(read, 128))
	wait := cmd.Wait()
	if err != nil || wait != nil || strings.TrimSpace(string(out)) != ProbeVersion {
		return ErrClosed
	}
	return nil
}
func transportEnvironment() []string {
	environment := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	if home, err := os.UserHomeDir(); err == nil {
		environment = append(environment, "HOME="+home)
	}
	return environment
}
