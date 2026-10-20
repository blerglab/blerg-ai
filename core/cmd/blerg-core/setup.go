package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

// firstRun runs at boot under the local provider. While no account exists core is in setup mode,
// and it logs one line saying so and how to finish: on the desktop (code-less setup allowed) open
// /setup on this machine; elsewhere get the setup link with `blerg-core setup-link`. It never
// mints a code and never logs a code or link: a log is read by more people and tools than the
// person who should create the admin.
func firstRun(ctx context.Context, local *authprovider.Local, publicURL string, allowLocal bool, logf func(string, ...any)) error {
	open, err := local.SetupOpen(ctx)
	if err != nil || !open {
		return err
	}
	if allowLocal {
		logf("Blerg is not set up yet: open %s/setup on this machine to create the first account.", strings.TrimRight(publicURL, "/"))
		return nil
	}
	logf("Blerg is not set up yet: the person who will own it runs `blerg-core setup-link` (in this container) for a one-time setup link, a credential, and opens it to create the first account.")
	return nil
}

// setupLink is the link a person opens to create the first account. The code rides in the URL
// fragment, which browsers never send to a server, so it stays out of proxy and ingress access
// logs. Without a known public URL it is the path alone, to be opened on core's own address.
func setupLink(publicURL, code string) string {
	return strings.TrimRight(publicURL, "/") + "/setup#code=" + url.QueryEscape(code)
}

// setupGateway is the extra peer a code-less /setup trusts besides loopback: the container's
// default gateway, but only when BLERG_SETUP_TRUST_DOCKER_GATEWAY=1 says core runs in a container
// with a published port (only the desktop compose file sets it). Outside a container the default
// gateway is the LAN router, which may present remote clients as itself; then nil, loopback only.
func setupGateway(getenv func(string) string, gateway func() net.IP) net.IP {
	if getenv("BLERG_SETUP_TRUST_DOCKER_GATEWAY") != "1" {
		return nil
	}
	return gateway()
}

// containerGateway is the default gateway from /proc/net/route, or nil. In a container with a
// published port, a connection from the host's own loopback arrives from this address.
func containerGateway() net.IP {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return defaultGateway(f)
}

// defaultGateway reads /proc/net/route's format: a header line, then "Iface Destination Gateway
// Flags ..." with addresses as little-endian hex. The default route has destination 00000000.
func defaultGateway(r io.Reader) net.IP {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 || (b[0]|b[1]|b[2]|b[3]) == 0 {
			continue
		}
		return net.IPv4(b[3], b[2], b[1], b[0])
	}
	return nil
}

// setupAllowLocal decides api.Deps.SetupAllowLocal: BLERG_SETUP_ALLOW_LOCAL=true, and only while
// BLERG_SETUP_PUBLISHED_ADDR (the host address the desktop compose file publishes core's port on)
// is a loopback address. A flag with any other published address is ignored, with a reason to
// log: other machines could reach /setup then, so the setup link is required.
func setupAllowLocal(getenv func(string) string) (bool, string) {
	if getenv("BLERG_SETUP_ALLOW_LOCAL") != "true" {
		return false, ""
	}
	addr := strings.TrimSuffix(strings.TrimPrefix(getenv("BLERG_SETUP_PUBLISHED_ADDR"), "["), "]")
	if addr == "localhost" {
		return true, ""
	}
	if ip := net.ParseIP(addr); ip != nil && ip.IsLoopback() {
		return true, ""
	}
	return false, fmt.Sprintf("BLERG_SETUP_ALLOW_LOCAL ignored: core is published on %q, not a loopback address, so first-run setup needs the setup link", addr)
}

// setupCompleteMessage is setup-link's answer once an account exists; installers match "setup is complete".
const setupCompleteMessage = "setup is complete: an account already exists. Sign in, or reset a password with `blerg-core users set-password --subject <username>`."

// runSetupLink implements `blerg-core setup-link`: while no account exists it mints a fresh setup
// code (the previous link stops working) and prints the setup link, alone, on stdout — so an
// installer can fetch it with `kubectl exec` instead of reading logs. Once an account exists it
// says setup is complete on stderr and exits 1; usage errors exit 2. `setup-link --check` mints
// nothing: it exits 0 (saying setup is open) or 1 (setup is complete), for an installer that must
// not print or invalidate a link.
func runSetupLink(args []string, stdout, stderr io.Writer) int {
	check := len(args) == 1 && args[0] == "--check"
	if len(args) != 0 && !check {
		_, _ = fmt.Fprintln(stderr, "usage: blerg-core setup-link [--check]")
		return 2
	}
	if authProviderName() != "local" {
		_, _ = fmt.Fprintf(stderr, "setup-link: first-run setup applies to the local provider only (BLERG_CORE_AUTH_PROVIDER=%s)\n", authProviderName())
		return 1
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		_, _ = fmt.Fprintln(stderr, "DATABASE_URL is required")
		return 1
	}
	ctx := context.Background()
	st, err := db.Open(ctx, dsn)
	if err != nil {
		log.Printf("open store: %v", err)
		return 1
	}
	defer st.Close()
	local := authprovider.NewLocal(st)
	if check {
		open, err := local.SetupOpen(ctx)
		if err != nil {
			log.Printf("setup-link: %v", err)
			return 1
		}
		if !open {
			_, _ = fmt.Fprintln(stderr, setupCompleteMessage)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "setup is open: no account exists yet")
		return 0
	}
	code, err := local.MintSetupCode(ctx)
	if errors.Is(err, authprovider.ErrSetupComplete) {
		_, _ = fmt.Fprintln(stderr, setupCompleteMessage)
		return 1
	}
	if err != nil {
		log.Printf("setup-link: %v", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, setupLink(os.Getenv("BLERG_CORE_PUBLIC_URL"), code))
	return 0
}
