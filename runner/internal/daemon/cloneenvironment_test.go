package daemon

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

func TestSandboxCloneEnvironmentCopiesOnlyPublicTrust(t *testing.T) {
	for _, key := range []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "SSL_CERT_DIR", "GIT_SSL_CAPATH"} {
		t.Setenv(key, "")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	secret := []byte("PRIVATE-KEY-CANARY")
	mixed := append(append([]byte{}, cert...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: secret})...)
	source := t.TempDir()
	trust := filepath.Join(source, "trust.pem")
	if err := os.WriteFile(trust, mixed, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(source, "ca")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abcd.0"), mixed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key"), secret, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", trust)
	t.Setenv("GIT_SSL_CAPATH", dir)
	t.Setenv("HTTPS_PROXY", "https://proxy-user:proxy-secret@proxy.example")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated-secret")
	t.Setenv("GIT_SSH_COMMAND", "unsafe-command")
	tmp := t.TempDir()
	args, err := sandboxCloneEnvironment(tmp)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{source, "proxy-secret", "unrelated-secret", "unsafe-command", "AWS_SECRET_ACCESS_KEY", "GIT_SSH_COMMAND"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment leaked %q", forbidden)
		}
	}
	for _, want := range []string{"-e HTTPS_PROXY", "-e SSL_CERT_FILE=/clone/trust-0/ca.pem", "-e GIT_SSL_CAPATH=/clone/trust-3"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s: %s", want, joined)
		}
	}
	for _, path := range []string{"trust-0/ca.pem", "trust-3/abcd.0"} {
		got, err := os.ReadFile(filepath.Join(tmp, path))
		if err != nil || !bytes.Equal(got, cert) || bytes.Contains(got, secret) {
			t.Fatalf("trust copy %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "trust-3/key")); !os.IsNotExist(err) {
		t.Fatal("copied private key")
	}
}

func TestSandboxCloneEnvironmentRejectsInvalidTrust(t *testing.T) {
	for _, key := range []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "SSL_CERT_DIR", "GIT_SSL_CAPATH"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "private.pem")
	if err := os.WriteFile(path, []byte("secret-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", path)
	if _, err := sandboxCloneEnvironment(t.TempDir()); !errors.Is(err, errCloneEnvironment) || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "secret-canary") {
		t.Fatal("invalid trust diagnostic", err)
	}
}

func TestNestedCloneCrashIsSweptFromRepositoriesRoot(t *testing.T) {
	if root := os.Getenv("BLG443_CRASH_CLONE_ROOT"); root != "" {
		cloneConnectedGit = func(_ context.Context, _ time.Duration, _ string, dest string, _ []string) error {
			if err := os.MkdirAll(dest, 0700); err != nil {
				panic(err)
			}
			os.Exit(0)
			return nil
		}
		_ = EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: true, Provider: "github", FullName: "example-org/example-repo", Folder: "work:2026/tools"})
		os.Exit(2)
	}
	root := t.TempDir()
	anchor := filepath.Join(root, "work:2026")
	if err := os.Mkdir(anchor, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNestedCloneCrashIsSweptFromRepositoriesRoot$")
	child.Env = append(os.Environ(), "BLG443_CRASH_CLONE_ROOT="+root)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v %s", err, out)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	staged := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cloneTmpPrefix) {
			staged++
		}
	}
	if staged != 1 {
		t.Fatalf("crashed clone not staged at swept root: %v", entries)
	}
	sweepCloneLeftovers(root, time.Now().Add(3*time.Hour))
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "work:2026" {
		t.Fatalf("sweep changed anchor or left partial: %v %v", entries, err)
	}
}

func TestConnectedSandboxCloneForwardsProxyAndTrust(t *testing.T) {
	for _, key := range []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "SSL_CERT_DIR", "GIT_SSL_CAPATH"} {
		t.Setenv(key, "")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	source := filepath.Join(t.TempDir(), "ca.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(source, cert, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", source)
	t.Setenv("HTTPS_PROXY", "https://proxy:secret@proxy.example")
	bin, log := fakeDocker(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", log)
	for _, clone := range []func(context.Context, time.Duration, string, string, gitprovider.Provider, string) error{sandboxGitCloneConnected, sandboxGitClone} {
		if err := clone(t.Context(), time.Second, "https://github.com/example-org/example-repo.git", t.TempDir(), gitprovider.NewGitHub(nil), "clone-token"); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HTTPS_PROXY", "SSL_CERT_FILE=/clone/trust-0/ca.pem"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatal("missing container environment", want)
		}
	}
	for _, secret := range []string{"proxy:secret", "clone-token", source} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("container argv leaked secret or host trust path")
		}
	}
}

func TestSandboxCloneCertificateFailureKeepsTrueCause(t *testing.T) {
	old, oldConnected := cloneInSandbox, cloneConnectedInSandbox
	oldTarget := cloneTargetGit
	t.Cleanup(func() { cloneInSandbox, cloneConnectedInSandbox, cloneTargetGit = old, oldConnected, oldTarget })
	cloneTargetGit = func(context.Context, time.Duration, string, string, []string) error {
		return errors.New("fixture: unexpected transport fallback")
	}
	fail := func(context.Context, time.Duration, string, string, gitprovider.Provider, string) error {
		return errCloneEnvironment
	}
	cloneInSandbox, cloneConnectedInSandbox = fail, fail
	for _, connected := range []bool{false, true} {
		root := t.TempDir()
		err := EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: connected, Sandbox: true, Token: "fixture-token", Provider: "github", FullName: "example-org/example-repo", Folder: "repo"})
		if !errors.Is(err, errCloneEnvironment) {
			t.Fatalf("certificate failure misreported, connected=%v: %v", connected, err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 0 {
			t.Fatal("certificate failure left partial clone", entries, readErr)
		}
	}
}
