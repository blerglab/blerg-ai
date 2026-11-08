package desktopupgrade

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasegate"
)

// VerifyServices checks the stamped build after services have completed startup
// (and therefore their migrations), without relying on the browser reconnecting.
func VerifyServices(ctx context.Context, run releasegate.Command, op Operation) error {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, service := range []struct{ Name, Path string }{{"blerg-core", "/api/site"}, {"blerg-board", "/api/version"}, {"blerg-runner", "/agents"}} {
		b, err := run(ctx, "docker", "compose", "port", service.Name, "8080")
		if err != nil {
			return err
		}
		// Consult only the local published port, never a configured remote hostname.
		line := strings.Split(strings.TrimSpace(string(b)), "\n")[0]
		host, port, err := net.SplitHostPort(line)
		if err != nil {
			return errors.New("service has no published local port")
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("invalid published service port")
		}
		if host == "0.0.0.0" || host == "::" || host == "" {
			host = os.Getenv("BLERG_BIND_ADDR")
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
		}
		if !localAddress(host) {
			return errors.New("published service address must belong to this host")
		}
		// #nosec G704 -- Published port and local interface address validated above; redirects are disabled.
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(host, port)+service.Path, nil)
		if err != nil {
			return err
		}
		// #nosec G704 -- Same validated local service URL, with redirects disabled.
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("upgraded service version is unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			return errors.New("upgraded service did not pass its version check")
		}
		var version struct{ Release, Commit, Version string }
		if json.Unmarshal(raw, &version) != nil {
			return errors.New("invalid service version response")
		}
		want := strings.TrimPrefix(op.Tag, "v")
		if service.Name == "blerg-runner" {
			if version.Version != want || len(version.Commit) < 7 || !strings.HasPrefix(op.NewCommit, version.Commit) {
				return errors.New("runner build does not match the selected release")
			}
		} else if version.Release != want || len(version.Commit) < 7 || !strings.HasPrefix(op.NewCommit, version.Commit) {
			return errors.New("service build does not match the selected release commit")
		}
	}
	return nil
}

// VerifyDaemonBinary confirms the installer built the daemon from this exact
// release. A reconnect from an old binary alone cannot establish upgrade success.
func VerifyDaemonBinary(root string, op Operation) error {
	info, err := buildinfo.ReadFile(filepath.Join(root, "bin/blerg-runner-daemon"))
	if err != nil {
		return errors.New("cannot verify installed daemon build")
	}
	if info.Path != "github.com/blerglab/blerg-ai/runner/cmd/daemon" {
		return errors.New("unexpected installed daemon executable")
	}
	for _, setting := range info.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		for _, field := range strings.Fields(setting.Value) {
			if field == "main.commit="+op.NewCommit {
				return nil
			}
		}
	}
	return errors.New("installed daemon was not built from the selected release")
}

func localAddress(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		candidate, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.Equal(candidate) {
			return true
		}
	}
	return false
}
