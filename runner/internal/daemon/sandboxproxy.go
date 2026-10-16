package daemon

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sandboxInternalHosts are the names a session container reaches on the sandbox network
// (the board and the runner server) plus the loopback names. They must never go through a
// corporate proxy, which cannot resolve them.
const sandboxInternalHosts = "blerg-runner,blerg-board,localhost,127.0.0.1"

// defaultDockerSocketHost is the host key the Docker client looks the `proxies` block up by
// when DOCKER_HOST is unset (docker/cli's default unix socket).
const defaultDockerSocketHost = "unix:///var/run/docker.sock"

// dockerClientNoProxy returns the `noProxy` the Docker client config would inject into a
// container, or "" when there is none or the file cannot be read (errors are ignored: a
// missing or odd config is the normal case).
//
// The client looks `proxies` up by the daemon host first and falls back to `default`
// (docker/cli ConfigFile.ParseProxyConfig), so this does the same. dockerConfigDir and host
// come from the environment: DOCKER_CONFIG (else ~/.docker) and DOCKER_HOST. The client also
// follows DOCKER_CONTEXT / the current context (Docker Desktop's own socket, say); that is not
// followed here, so a per-host key for a context endpoint is missed and `default` is used.
func dockerClientNoProxy(getenv func(string) string, home string) string {
	dir := getenv("DOCKER_CONFIG")
	if dir == "" {
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, ".docker")
	}
	f, err := os.Open(filepath.Join(dir, "config.json")) //nolint:gosec // the Docker client's own config file; DOCKER_CONFIG is the daemon owner's environment
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20)) // a config file is a few lines; bound it anyway
	if err != nil {
		return ""
	}
	var cfg struct {
		Proxies map[string]struct {
			NoProxy string `json:"noProxy"`
		} `json:"proxies"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	host := getenv("DOCKER_HOST")
	if host == "" {
		host = defaultDockerSocketHost
	}
	if p, ok := cfg.Proxies[host]; ok {
		return p.NoProxy
	}
	return cfg.Proxies["default"].NoProxy
}

// sandboxNoProxy is the value both NO_PROXY and no_proxy get inside a sandbox on the desktop
// network: the union (order kept, no duplicates) of what the daemon's own environment has,
// what the Docker client config would inject, and the names of the services a session talks
// to. Docker injects the config's proxy variables into every container it starts, and a
// corporate proxy cannot reach `blerg-runner`; adding the service names to the list the
// person already has keeps their own exceptions working.
func sandboxNoProxy(getenv func(string) string, home string) string {
	var out []string
	seen := map[string]bool{}
	add := func(list string) {
		for _, h := range strings.Split(list, ",") {
			h = strings.TrimSpace(h)
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	add(getenv("NO_PROXY"))
	add(getenv("no_proxy"))
	add(dockerClientNoProxy(getenv, home))
	add(sandboxInternalHosts)
	// "*" (bypass everything) only works as the whole value in curl and Python's urllib.
	if seen["*"] {
		return "*"
	}
	return strings.Join(out, ",")
}
