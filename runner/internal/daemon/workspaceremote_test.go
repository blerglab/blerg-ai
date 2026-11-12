package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Docker Desktop presents the mounted host key as root-owned. The worker must
// consume stdin authority without statting that mount; this fixture makes the
// mounted path unavailable, a stronger constraint than remapping its owner.
func TestWorkspaceQuotaStdinAuthority(t *testing.T) {
	raw := `{"Action":"quota-unknown","QuotaUID":501,"QuotaGID":20,"Record":"/unavailable-mounted-state/session.json","QuotaKey":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","State":{"Session":"quota-fixture"}}`
	var request workspaceWorkerRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceWorker(t.Context(), request)
	if response.Error != "unknown quota request" {
		t.Fatalf("stdin authority did not reach quota dispatch: %s", response.Error)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"0123456789abcdef0123456789abcdef", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("authority leaked in worker response")
		}
	}
}

// A synthetic stat lets an unprivileged Linux test emulate a Desktop mount
// owned by 0:0, and independently test a host file owned by another user.
type quotaOwnerInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i quotaOwnerInfo) Sys() any { return &i.stat }

func TestWorkspaceQuotaHostOwner(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	key, err := workspaceManifestKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatal("invalid fixture authority")
	}
	for _, owner := range []uint32{0, uint32(os.Getuid() + 1)} {
		if owner == uint32(os.Getuid()) {
			continue
		}
		_, err := workspaceManifestKeyWithStat(func(path string) (os.FileInfo, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			stat := *info.Sys().(*syscall.Stat_t)
			stat.Uid, stat.Gid = owner, 0
			return quotaOwnerInfo{info, stat}, nil
		})
		if err == nil {
			t.Fatal("wrong host owner accepted")
		}
	}
}

func TestWorkspaceQuotaKeeperStdinLaunch(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	key, err := workspaceManifestKey()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nexec \"$QUOTA_TEST_BINARY\" -test.run=^TestHelperQuotaKeeperFixture$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("QUOTA_TEST_BINARY", binary)
	t.Setenv("QUOTA_TEST_FIXTURE", "1")
	t.Setenv(testStateDirEnv, os.Getenv(DaemonStateDirEnv))
	storage := &DockerWorkspaceStorage{}
	r := workspaceRemoteRecord{Session: "quota-fixture", Token: "fixture-token", Context: "fixture", Container: strings.Repeat("a", 64)}
	var logs bytes.Buffer
	oldWriter, oldLogger := log.Writer(), slog.Default()
	log.SetOutput(&logs)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger); log.SetOutput(oldWriter) })
	response, err := storage.call(t.Context(), r, "quota-allocate")
	if err != nil {
		t.Fatal(err)
	}
	if response.QuotaDevice != "/dev/loop7" {
		t.Fatal("keeper launch failed")
	}
	// The only new host file is the original authority: no copied keyring.
	entries, err := os.ReadDir(sandboxCloneRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "workspace-integrity.key" {
		t.Fatal("unexpected host authority copy")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{string(key), base64.StdEncoding.EncodeToString(key)} {
		if strings.Contains(logs.String(), secret) || bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("quota key leaked in logs or response")
		}
	}
}

// TestHelperQuotaKeeperFixture is the quota keeper subprocess, not a test: its name
// stays outside the macOS gate's "-run TestWorkspace" selection, which refuses skips.
func TestHelperQuotaKeeperFixture(t *testing.T) {
	if os.Getenv("QUOTA_TEST_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	args := strings.Join(os.Args, " ")
	if !strings.Contains(args, " exec ") {
		if strings.Contains(args, ".State.Running") {
			fmt.Print("true")
		} else {
			fmt.Print("fixture-token")
		}
		os.Exit(0)
	}
	var request workspaceWorkerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(21)
	}
	hostKey, err := workspaceManifestKey()
	if err != nil || len(request.QuotaKey) != 32 || !bytes.Equal(hostKey, request.QuotaKey) {
		os.Exit(22)
	}
	for _, secret := range []string{string(request.QuotaKey), base64.StdEncoding.EncodeToString(request.QuotaKey)} {
		if strings.Contains(args, secret) {
			os.Exit(23)
		}
		for _, env := range os.Environ() {
			if strings.Contains(env, secret) {
				os.Exit(24)
			}
		}
	}
	// Emulate Docker Desktop's 0:0 stat for the mounted key. This would fail
	// the old keeper owner check for a normal host UID; no mounted-key stat
	// participates in worker authentication now.
	info, err := os.Lstat(filepath.Join(sandboxCloneRoot(), "workspace-integrity.key"))
	if err != nil {
		os.Exit(32)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid, stat.Gid = 0, 0
	mounted := quotaOwnerInfo{info, stat}
	if mounted.Sys().(*syscall.Stat_t).Uid != 0 || (os.Getuid() != 0 && ownedByDaemon(mounted)) {
		os.Exit(33)
	}
	// Simulate a keeper whose bind-mount view cannot authenticate the host owner.
	// No mounted host key is available; its in-memory journal authority must work.
	if err := os.Setenv(DaemonStateDirEnv, filepath.Join(t.TempDir(), "unavailable-mount")); err != nil {
		os.Exit(25)
	}
	s := &FilesystemWorkspaceStorage{Root: t.TempDir(), authorityKey: request.QuotaKey}
	directory := filepath.Join(s.Root, request.State.Session)
	if err := os.MkdirAll(directory, 0700); err != nil {
		os.Exit(26)
	}
	record := workspaceQuotaRecord{Version: 1, Session: request.State.Session, Budget: request.QuotaBudget, Phase: "ready"}
	if err := s.saveQuota(directory, record); err != nil {
		os.Exit(27)
	}
	if recovered, err := s.readQuota(directory); err != nil || recovered != record {
		os.Exit(28)
	}
	if _, err := os.Stat(sandboxCloneRoot()); !os.IsNotExist(err) {
		os.Exit(29)
	}
	// Exercise actual quota request dispatch without requiring loop mounts.
	request.Action = "quota-unknown"
	response := executeWorkspaceWorker(t.Context(), request)
	if response.Error != "unknown quota request" {
		os.Exit(30)
	}
	// stderr must never escape the docker request transport.
	if _, err := os.Stderr.Write(request.QuotaKey); err != nil {
		os.Exit(34)
	}
	response.Error = ""
	response.QuotaDevice = "/dev/loop7"
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(31)
	}
	os.Exit(0)
}

func TestWorkspaceQuotaInvalidAuthority(t *testing.T) {
	for _, key := range [][]byte{nil, {}, make([]byte, 31), make([]byte, 33)} {
		response := executeWorkspaceWorker(t.Context(), workspaceWorkerRequest{Action: "quota-allocate", QuotaKey: key, State: workspacePrivateManifest{Session: "fixture"}})
		if response.Error != "invalid quota authority" {
			t.Fatal("invalid authority reached storage")
		}
	}
}

func TestWorkspaceQuotaMemoryAuthorityBinding(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, filepath.Join(t.TempDir(), "unavailable"))
	s := &FilesystemWorkspaceStorage{authorityKey: bytes.Repeat([]byte{1}, 32)}
	directory := t.TempDir()
	record := workspaceQuotaRecord{Version: 1, Session: "fixture"}
	if err := s.saveQuota(directory, record); err != nil {
		t.Fatal(err)
	}
	if got, err := s.readQuota(directory); err != nil || got != record {
		t.Fatal("memory authority lost")
	}
	s.authorityKey = bytes.Repeat([]byte{2}, 32)
	if _, err := s.readQuota(directory); err == nil {
		t.Fatal("changed authority accepted")
	}
	if _, err := os.Stat(sandboxCloneRoot()); !os.IsNotExist(err) {
		t.Fatal("key copied to disk")
	}
}
