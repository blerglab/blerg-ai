//go:build darwin || linux

package hostgit

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	memorySampleTimeout = 100 * time.Millisecond
	memorySampleOutput  = 64 << 10
)

// Darwin ps selects process groups with -g (Linux ps uses a different selector). Keep the
// construction/parser available on Linux for fake-ps tests, without using it for Linux Git.
// Only numeric RSS is requested: no repository paths, argv or environment are collected.
func darwinRSSCommand(ctx context.Context, pgid int) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-g", strconv.Itoa(pgid)) //nolint:gosec // G204: fixed system binary/flags; the only variable is our started child's numeric process group.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = memorySampleTimeout
	return cmd
}

func readGroupRSS(ctx context.Context, pgid int, command func(context.Context, int) *exec.Cmd) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, memorySampleTimeout)
	defer cancel()
	cmd := command(ctx, pgid)
	stdout := &capped{max: memorySampleOutput}
	stderr := &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if stdout.overflow || stderr.overflow {
		return 0, errors.New("RSS sample output exceeded its limit")
	}
	if stderr.buf.Len() != 0 {
		// Darwin ps can report a sysctl failure on stderr while returning success.
		return 0, errors.New("RSS sample command reported an error")
	}
	if err != nil {
		// ps exits 1 with no output when the group has just exited. Do not turn a short-lived
		// successful Git command into a refusal while Wait is completing.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stdout.buf.Len() == 0 && stderr.buf.Len() == 0 {
			return 0, nil
		}
		return 0, errors.New("RSS sample command failed")
	}
	if strings.TrimSpace(stdout.buf.String()) == "" {
		return 0, errors.New("RSS sample command returned no data")
	}
	return parseGroupRSS(stdout.buf.String())
}

func parseGroupRSS(raw string) (uint64, error) {
	var total uint64
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rss, err := strconv.ParseUint(line, 10, 64)
		if err != nil || rss > ^uint64(0)-total {
			return 0, errors.New("invalid RSS sample")
		}
		total += rss
	}
	return total, nil
}
