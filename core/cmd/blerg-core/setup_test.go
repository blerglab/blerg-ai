package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type logCapture struct{ lines []string }

func (c *logCapture) logf(format string, args ...any) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func setupOpen(t *testing.T, local *authprovider.Local) bool {
	t.Helper()
	open, err := local.SetupOpen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return open
}

var linkRe = regexp.MustCompile(`^https://blerg\.example\.com/setup#code=([A-Za-z0-9_-]{43})$`)

// The first boot with no account creates none, mints no code, and logs one line that says how to
// finish setup without carrying a code or link.
func TestFirstRunLogsHowToFinishSetupButNoLink(t *testing.T) {
	st := mainTestStore(t)
	local := authprovider.NewLocal(st)
	ctx := context.Background()
	for _, allowLocal := range []bool{false, true} {
		var logs logCapture
		if err := firstRun(ctx, local, "https://blerg.example.com/", allowLocal, logs.logf); err != nil {
			t.Fatal(err)
		}
		if !setupOpen(t, local) {
			t.Fatal("first boot created an account; it must leave setup open")
		}
		if len(logs.lines) != 1 {
			t.Fatalf("allowLocal=%v: logged %d lines, want 1: %q", allowLocal, len(logs.lines), logs.lines)
		}
		line := logs.lines[0]
		if strings.Contains(line, "code=") || strings.Contains(line, "#") {
			t.Fatalf("boot log carries a setup code: %q", line)
		}
		want := "blerg-core setup-link"
		if allowLocal {
			want = "https://blerg.example.com/setup on this machine"
		}
		if !strings.Contains(line, want) {
			t.Fatalf("allowLocal=%v: %q does not say %q", allowLocal, line, want)
		}
	}
	var codes int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM setup_code`).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("boot minted %d setup codes (%v), want none", codes, err)
	}
	// Set up: a later boot logs nothing.
	if _, err := st.Pool().Exec(ctx, `INSERT INTO accounts (provider, provider_subject, role) VALUES ('local', 'owner', 'admin')`); err != nil {
		t.Fatal(err)
	}
	var logs logCapture
	if err := firstRun(ctx, authprovider.NewLocal(st), "https://blerg.example.com", false, logs.logf); err != nil {
		t.Fatal(err)
	}
	if len(logs.lines) != 0 {
		t.Fatalf("a set-up install logged %q", logs.lines)
	}
}

func TestDefaultGatewayFromProcNetRoute(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t016433C6\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t006433C6\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	if got := defaultGateway(strings.NewReader(route)); !got.Equal(net.ParseIP("198.51.100.1")) {
		t.Fatalf("gateway %v, want 198.51.100.1", got)
	}
	if got := defaultGateway(strings.NewReader("Iface\tDestination\tGateway\neth0\t006433C6\t00000000\n")); got != nil {
		t.Fatalf("no default route: %v, want nil", got)
	}
}

func TestSetupAllowLocalNeedsTheFlagAndALoopbackAddress(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want bool
		warn bool
	}{
		{nil, false, false},
		{map[string]string{"BLERG_SETUP_PUBLISHED_ADDR": "127.0.0.1"}, false, false},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "1", "BLERG_SETUP_PUBLISHED_ADDR": "127.0.0.1"}, false, false},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true", "BLERG_SETUP_PUBLISHED_ADDR": "127.0.0.1"}, true, false},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true", "BLERG_SETUP_PUBLISHED_ADDR": "::1"}, true, false},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true", "BLERG_SETUP_PUBLISHED_ADDR": "localhost"}, true, false},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true", "BLERG_SETUP_PUBLISHED_ADDR": "0.0.0.0"}, false, true},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true", "BLERG_SETUP_PUBLISHED_ADDR": "192.0.2.10"}, false, true},
		{map[string]string{"BLERG_SETUP_ALLOW_LOCAL": "true"}, false, true},
	} {
		got, why := setupAllowLocal(envFrom(tc.env))
		if got != tc.want || (why != "") != tc.warn {
			t.Errorf("%v: allow=%v warn=%q, want allow=%v warn=%v", tc.env, got, why, tc.want, tc.warn)
		}
	}
}

func TestSetupLinkCLI(t *testing.T) {
	st := mainTestStore(t)
	t.Setenv("DATABASE_URL", st.Pool().Config().ConnString())
	t.Setenv("BLERG_CORE_AUTH_PROVIDER", "")
	t.Setenv("BLERG_CORE_PUBLIC_URL", "https://blerg.example.com")
	var out, errOut bytes.Buffer
	if code := runSetupLink([]string{"extra"}, &out, &errOut); code != 2 {
		t.Fatalf("extra argument: exit %d, want 2", code)
	}
	ctx := context.Background()
	codes := func() int {
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM setup_code`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// --check reports open and mints nothing.
	out.Reset()
	if code := runSetupLink([]string{"--check"}, &out, &errOut); code != 0 || codes() != 0 || strings.Contains(out.String(), "code") {
		t.Fatalf("--check while open: exit %d, %d codes minted, stdout %q", code, codes(), out.String())
	}
	out.Reset()
	if code := runSetupLink(nil, &out, &errOut); code != 0 {
		t.Fatalf("setup-link exit %d: %s", code, errOut.String())
	}
	line := strings.TrimSuffix(out.String(), "\n")
	m := linkRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("stdout is not the link alone, with the code in the fragment (%d bytes)", out.Len())
	}
	first := m[1]
	// --check does not supersede the link.
	if code := runSetupLink([]string{"--check"}, io.Discard, &errOut); code != 0 {
		t.Fatalf("--check exit %d", code)
	}
	// A second call supersedes the first link.
	out.Reset()
	if code := runSetupLink(nil, &out, &errOut); code != 0 {
		t.Fatalf("second setup-link exit %d", code)
	}
	m2 := linkRe.FindStringSubmatch(strings.TrimSpace(out.String()))
	if m2 == nil {
		t.Fatal("second link malformed")
	}
	local := authprovider.NewLocal(st)
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Code: first}); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("superseded CLI code: %v, want ErrSetupCode", err)
	}
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Code: m2[1]}); err != nil {
		t.Fatalf("latest CLI code: %v", err)
	}
	// Set up: nothing on stdout, a reason on stderr, non-zero exit.
	out.Reset()
	errOut.Reset()
	if code := runSetupLink(nil, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "setup is complete") {
		t.Fatalf("after setup: exit %d stdout %q stderr %q", code, out.String(), errOut.String())
	}
	errOut.Reset()
	if code := runSetupLink([]string{"--check"}, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "setup is complete") {
		t.Fatalf("--check after setup: exit %d stdout %q stderr %q", code, out.String(), errOut.String())
	}
	// Not the local provider: refused.
	t.Setenv("BLERG_CORE_AUTH_PROVIDER", "oidc")
	if code := runSetupLink(nil, &out, &errOut); code != 1 || out.Len() != 0 {
		t.Fatalf("oidc provider: exit %d stdout %q", code, out.String())
	}
}

func TestSetupGatewayOnlyWhenComposeSaysContainer(t *testing.T) {
	gw := func() net.IP { return net.ParseIP("198.51.100.1") }
	if got := setupGateway(envFrom(nil), gw); got != nil {
		t.Fatalf("no BLERG_SETUP_TRUST_DOCKER_GATEWAY: trusted %v, want loopback only", got)
	}
	if got := setupGateway(envFrom(map[string]string{"BLERG_SETUP_TRUST_DOCKER_GATEWAY": "true"}), gw); got != nil {
		t.Fatalf("a value other than 1: trusted %v", got)
	}
	if got := setupGateway(envFrom(map[string]string{"BLERG_SETUP_TRUST_DOCKER_GATEWAY": "1"}), gw); !got.Equal(net.ParseIP("198.51.100.1")) {
		t.Fatalf("with the flag: %v, want the gateway", got)
	}
}
