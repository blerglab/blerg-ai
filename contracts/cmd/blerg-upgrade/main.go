// blerg-upgrade is the desktop installer's host-side upgrade worker.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopupgrade"
	"github.com/blerglab/blerg-ai/contracts/releasegate"
)

func main() {
	fail := func(e error) { fmt.Fprintln(os.Stderr, "upgrade:", e); os.Exit(1) }
	tag := ""
	if len(os.Args) > 2 {
		fail(fmt.Errorf("usage: --upgrade [vX.Y.Z]"))
	}
	if len(os.Args) == 2 {
		tag = os.Args[1]
	}
	if tag != "" {
		if _, err := releasegate.ParseVersion(tag); err != nil || strings.Contains(tag, "-rc.") {
			fail(fmt.Errorf("upgrade accepts published stable vX.Y.Z tags only"))
		}
	}
	ctx, cancel := upgradeContext()
	defer cancel()
	b, err := releasegate.Exec(ctx, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		fail(err)
	}
	root := string(b)
	for len(root) > 0 && root[len(root)-1] == '\n' {
		root = root[:len(root)-1]
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fail(err)
	}
	hash := sha256.Sum256([]byte(root))
	state := filepath.Join(home, ".blerg-upgrades", hex.EncodeToString(hash[:8]))
	daemonState, err := desktopupgrade.DaemonDirectory(root, home, os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR"))
	if err != nil {
		fail(err)
	}
	run := desktopupgrade.Executor(root, daemonState)
	// Production releases always come from the public project, never a URL
	// supplied by a browser or a locally shadowed tag. Fetch origin must match.
	url, err := run(ctx, "git", "remote", "get-url", "origin")
	if err != nil {
		fail(err)
	}
	upstream := string(url)
	if upstream != "https://github.com/blerglab/blerg-ai.git\n" && upstream != "git@github.com:blerglab/blerg-ai.git\n" {
		fail(fmt.Errorf("origin must be the official public Blerg repository"))
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	releases := func(ctx context.Context) ([]desktopupgrade.Release, error) {
		var all []desktopupgrade.Release
		for page := 1; page <= 20; page++ {
			req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.github.com/repos/blerglab/blerg-ai/releases?per_page=100&page=%d", page), nil)
			if e != nil {
				return nil, e
			}
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, e := client.Do(req)
			if e != nil {
				return nil, fmt.Errorf("release metadata is unavailable")
			}
			raw, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if e != nil || resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("release metadata request failed")
			}
			var rows []desktopupgrade.Release
			if json.Unmarshal(raw, &rows) != nil {
				return nil, fmt.Errorf("invalid release metadata")
			}
			all = append(all, rows...)
			if len(rows) < 100 {
				return all, nil
			}
		}
		return nil, fmt.Errorf("release pagination limit exceeded")
	}
	u := desktopupgrade.Upgrader{Root: root, State: state, Home: home, DaemonState: daemonState, Run: run, Releases: releases, Dump: desktopupgrade.DumpExecutor(root), Progress: func(s string) { fmt.Println("upgrade:", s) }}
	fmt.Println("Upgrade recovery record:", filepath.Join(state, "operation.json"))
	if err = u.Upgrade(ctx, tag); err != nil {
		fail(err)
	}
}

func upgradeContext() (context.Context, context.CancelFunc) {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	ctx, cancel := context.WithTimeout(signalCtx, 2*time.Hour)
	return ctx, func() { cancel(); stop() }
}
