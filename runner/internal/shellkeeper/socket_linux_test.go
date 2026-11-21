//go:build linux

package shellkeeper

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"golang.org/x/sys/unix"
)

func keeperFixture(t *testing.T) (string, *Server, Epoch) {
	t.Helper()
	dir := provision(t)
	s, err := Listen(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return dir, s, a.Epoch
}

func TestMutualInventoryAndSingleton(t *testing.T) {
	dir, _, epoch := keeperFixture(t)
	if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
		t.Fatal(err)
	}
	if second, err := Listen(t.Context(), dir); err == nil {
		_ = second.Close()
		t.Fatal("second keeper stole socket")
	}
	if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
		t.Fatal("losing singleton attempt disturbed live keeper", err)
	}
}

func TestInventoryIsNotReconciliationProof(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	s.mu.Lock()
	s.snapshot = func() []Candidate { return []Candidate{{Allocation: "allocation", Custody: "custody", Shell: "shell"}} }
	s.mu.Unlock()
	if candidates, err := Inspect(t.Context(), dir, epoch); err != nil || len(candidates) != 1 {
		t.Fatal("authenticated candidate list", err)
	}
	if RequireEmpty(t.Context(), dir, epoch) == nil {
		t.Fatal("unreconciled writers passed the collection fence")
	}
}

func TestSignedEpochRollbackAndForkRefusedOverSocket(t *testing.T) {
	dir, _, epoch := keeperFixture(t)
	if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
		t.Fatal(err)
	}
	i, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Epoch{{Number: epoch.Number - 1, ID: strings.Repeat("a", 64)}, {Number: epoch.Number, ID: strings.Repeat("b", 64)}} {
		e.Signature = sign(i.Daemon, "epoch", epochBody(e))
		if _, err := Inspect(t.Context(), dir, e); err == nil {
			t.Fatal("stale epoch admitted")
		}
	}
}

func TestKeeperIdentityReplacementRefused(t *testing.T) {
	dir, _, epoch := keeperFixture(t)
	i, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, i.Keeper, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a socket takeover with a different pinned identity. The live
	// keeper still has its original key; the client must fail its proof.
	if err = writePrivate(dir, "daemon-key.json", roleKey{Key: i.Daemon, Peer: publicKey(i.Keeper)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = Inspect(t.Context(), dir, epoch); err == nil {
		t.Fatal("unrecognized keeper identity accepted")
	}
}

func TestHelloRefusesVersionSignatureAndReplay(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	i, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	var replay message
	for _, name := range []string{"capture", "replay", "wrong_signature", "wrong_version", "drain_wrong_signature"} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			// A bounded guard, never a progress signal.
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			w := newWire(conn)
			version := Version
			if name == "wrong_version" {
				version++
			}
			secret, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			build, err := BuildID()
			if err != nil {
				t.Fatal(err)
			}
			operation := ""
			if name == "drain_wrong_signature" {
				operation = "drain"
				version = maintenanceVersion
			}
			if err = w.write(message{Proof: transcript{Operation: operation, Version: version, ClientNonce: strings.Repeat("a", 64), Epoch: epoch, ClientPublic: secret.PublicKey().Bytes(), ClientBuild: build}}); err != nil {
				t.Fatal(err)
			}
			challenge, err := w.read()
			if name == "wrong_version" {
				if err == nil {
					t.Fatal("incompatible version accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			auth := message{Proof: challenge.Proof, Signature: sign(i.Daemon, "daemon", challenge.Proof)}
			switch name {
			case "capture":
				replay = auth
			case "replay":
				auth = replay
			case "wrong_signature", "drain_wrong_signature":
				auth.Signature = sign(i.Keeper, "daemon", challenge.Proof)
			}
			if err = w.write(auth); err != nil {
				t.Fatal(err)
			}
			_, err = w.read()
			if (name == "capture") != (err == nil) {
				t.Fatalf("inventory authorization result: %v", err)
			}
		})
	}
	if s.Drained() {
		t.Fatal("unsigned drain stopped keeper")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch.Number != epoch.Number {
		t.Fatal("invalid proof changed authority")
	}
}

func TestPeerUIDProofRefusesMissingZeroAndWrongCredentials(t *testing.T) {
	for _, peer := range []*unix.Ucred{nil, {}, {Pid: 42, Uid: 0}, {Pid: 42, Uid: 1001}} {
		if validPeer(peer, 1000) {
			t.Fatal("unproven peer accepted")
		}
	}
	if !validPeer(&unix.Ucred{Pid: 42, Uid: 1000}, 1000) || !validPeer(&unix.Ucred{Pid: 42, Uid: 0}, 0) {
		t.Fatal("kernel peer identity refused")
	}
}

func TestInventoryBoundsAndFailureLogNeverIncludePeerContent(t *testing.T) {
	for _, list := range [][]Candidate{
		{{Allocation: "allocation", Custody: "custody"}},
		{{Allocation: "allocation", Custody: "custody", Shell: "shell"}, {Allocation: "other", Custody: "other", Shell: "shell"}},
		{{Allocation: strings.Repeat("a", 129), Custody: "custody", Shell: "shell"}},
		make([]Candidate, maxCandidates+1),
	} {
		if validCandidates(list) {
			t.Fatal("invalid inventory admitted")
		}
	}
	var log bytes.Buffer
	prior := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&log, nil)))
	defer func() { failurelog.Default = prior }()
	dir, s, epoch := keeperFixture(t)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	w := newWire(conn)
	if err = w.write(message{Proof: transcript{Version: Version, ClientNonce: "token=fixture-private-content", Epoch: epoch}}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.read(); err == nil {
		t.Fatal("malformed hello admitted")
	}
	_ = s.Close() // join all failure logging before inspecting/restoring the recorder
	if strings.Contains(log.String(), "fixture-private-content") || strings.Contains(log.String(), dir) || !strings.Contains(log.String(), "hello_shape_version") || !strings.Contains(log.String(), `"cause"`) {
		t.Fatalf("unsafe or missing failure cause: %q", log.String())
	}
}

func TestClientRefusesSignedIncompatibleAndForgedKeeperReplies(t *testing.T) {
	for _, name := range []string{"keeper_version", "keeper_signature", "inventory_signature", "inventory_mac", "inventory_replay", "inventory_duplicate", "inventory_huge", "close_after_challenge", "close_after_auth", "valid"} {
		t.Run(name, func(t *testing.T) {
			dir := provision(t)
			i, err := LoadIdentity(dir)
			if err != nil {
				t.Fatal(err)
			}
			a, err := AcquireDaemon(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			path := filepath.Join(dir, "keeper.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			if err = os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			ended := make(chan error, 1)
			go func() {
				ended <- func() error {
					conn, err := listener.AcceptUnix()
					if err != nil {
						return err
					}
					defer func() { _ = conn.Close() }()
					if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
						return err
					}
					w := newWire(conn)
					hello, err := w.read()
					if err != nil {
						return err
					}
					p := hello.Proof
					p.ServerNonce, p.Generation = strings.Repeat("b", 64), strings.Repeat("c", 64)
					secret, err := ecdh.X25519().GenerateKey(rand.Reader)
					if err != nil {
						return err
					}
					p.KeeperPublic = secret.PublicKey().Bytes()
					p.KeeperBuild = p.ClientBuild
					if name == "keeper_version" {
						p.Version++
					}
					key := i.Keeper
					if name == "keeper_signature" {
						key = i.Daemon
					}
					if err = w.write(message{Proof: p, Signature: sign(key, "keeper", p)}); err != nil {
						return err
					}
					if name == "keeper_version" || name == "keeper_signature" {
						if _, err := w.read(); !errors.Is(err, io.ErrUnexpectedEOF) {
							return errors.New("daemon proof released to unverified keeper")
						}
						return nil
					}
					if name == "close_after_challenge" {
						return nil
					}
					if _, err = w.read(); err != nil {
						return err
					}
					if name == "close_after_auth" {
						return nil
					}
					var candidates []Candidate
					switch name {
					case "inventory_signature":
						key = i.Daemon
					case "inventory_replay":
						p.ServerNonce = strings.Repeat("d", 64)
					case "inventory_duplicate":
						candidates = []Candidate{{Allocation: "allocation", Custody: "custody", Shell: "shell"}, {Allocation: "allocation", Custody: "custody", Shell: "shell"}}
					case "inventory_huge":
						candidates = make([]Candidate, maxCandidates+1)
					}
					shared, err := sessionKey(secret, p.ClientPublic, p)
					if err != nil {
						return err
					}
					inv := inventory{Proof: p, Candidates: candidates}
					mac := inventoryMAC(shared, inv)
					if name == "inventory_mac" {
						mac = []byte("relay injection")
					}
					return w.write(message{Proof: p, Candidates: candidates, Signature: sign(key, "inventory", inv), MAC: mac})
				}()
			}()
			_, result := inspect(t.Context(), dir, a.Epoch)
			if err = <-ended; err != nil {
				t.Fatal(err)
			}
			if (result == nil) != (name == "valid") {
				t.Fatalf("keeper proof: %v", result)
			}
			if strings.HasPrefix(name, "close_after_") && !errors.Is(result, errBusy) {
				t.Fatalf("post-challenge I/O must retry: %v", result)
			}
		})
	}
}

func TestMissingUnsafeAndUnavailableSocketNeverPassFence(t *testing.T) {
	for _, name := range []string{"missing", "wide", "symlink", "regular", "stopped", "canceled"} {
		t.Run(name, func(t *testing.T) {
			dir, s, epoch := keeperFixture(t)
			path := filepath.Join(dir, "keeper.sock")
			ctx := t.Context()
			var err error
			switch name {
			case "wide":
				err = os.Chmod(path, 0666)
			case "missing", "stopped", "symlink", "regular":
				err = s.Close()
				if name == "symlink" && err == nil {
					err = os.Symlink(filepath.Join(dir, "daemon-key.json"), path)
				}
				if name == "regular" && err == nil {
					err = os.WriteFile(path, nil, 0600)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if RequireEmpty(ctx, dir, epoch) == nil {
				t.Fatal("unproven empty inventory passed fence")
			}
		})
	}
}
