//go:build linux

package shellkeeper

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedChannelRejectsInjectionReplayAndDirection(t *testing.T) {
	for _, name := range []string{"valid", "unsigned", "wrong_direction", "replay", "corrupt_body", "overflow"} {
		t.Run(name, func(t *testing.T) {
			server, client := localPair(t)
			key := bytes.Repeat([]byte{7}, 32)
			ch := newChannel(server, key, "client", "keeper", nil)
			f := streamFrame{Sequence: 1, Body: json.RawMessage(`{"action":"inspect"}`)}
			f.MAC = streamMAC(key, "client", f)
			switch name {
			case "unsigned":
				f.MAC = nil
			case "wrong_direction":
				f.MAC = streamMAC(key, "keeper", f)
			case "replay":
				ch.received = 1
			case "corrupt_body":
				f.Body = json.RawMessage(`{"action":"launch"}`)
			case "overflow":
				ch.received = ^uint64(0)
			}
			sent := make(chan error, 1)
			go func() { sent <- json.NewEncoder(client).Encode(f) }()
			var result struct{ Action string }
			err := ch.Receive(&result)
			if (err == nil) != (name == "valid") {
				t.Fatal("stream authentication", err)
			}
			if err = <-sent; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthenticatedChannelClearsExpiredHandshakeDeadline(t *testing.T) {
	server, client := localPair(t)
	key := bytes.Repeat([]byte{1}, 32)
	if err := server.SetDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	channel, err := authenticatedChannel(server, key, "client", "keeper", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := streamFrame{Sequence: 1, Body: json.RawMessage(`"ready"`)}
	frame.MAC = streamMAC(key, "client", frame)
	sent := make(chan error, 1)
	go func() { sent <- json.NewEncoder(client).Encode(frame) }()
	var reply string
	if err = channel.Receive(&reply); err != nil || reply != "ready" {
		t.Fatal("pre-auth deadline expired an authenticated session", err)
	}
	if err = <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestServiceChannelPinsGenerationBeforeAuthentication(t *testing.T) {
	dir := provision(t)
	calls := 0
	server, err := ListenService(t.Context(), dir, nil, func(_ context.Context, ch *Channel) error {
		calls++
		var body string
		if err := ch.Receive(&body); err != nil {
			return err
		}
		return ch.Send(body)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	wrong := strings.Repeat("f", 64)
	if wrong == server.generation {
		wrong = strings.Repeat("e", 64)
	}
	if err = WithSession(t.Context(), dir, authority.Epoch, wrong, func(*Channel, Snapshot) error { t.Fatal("wrong generation acquired a channel"); return nil }); err == nil {
		t.Fatal("generation was not pinned")
	}
	if calls != 0 {
		t.Fatal("daemon proof released to replacement generation")
	}
	err = WithSession(t.Context(), dir, authority.Epoch, server.generation, func(ch *Channel, _ Snapshot) error {
		if err := ch.Send("generation-bound"); err != nil {
			return err
		}
		var reply string
		if err := ch.Receive(&reply); err != nil {
			return err
		}
		if reply != "generation-bound" {
			t.Error("wrong channel response")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceChannelAcceptsCompatibleDifferentBuildWithPinnedGeneration(t *testing.T) {
	dir := provision(t)
	server, err := ListenService(t.Context(), dir, nil, func(context.Context, *Channel) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	server.mu.Lock()
	server.build = strings.Repeat("f", 64)
	server.mu.Unlock()
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	if err = WithSession(t.Context(), dir, authority.Epoch, server.generation, func(*Channel, Snapshot) error { return nil }); err != nil {
		t.Fatal("compatible executable refused", err)
	}
}

func TestServiceStreamRetainsHandshakeReadAhead(t *testing.T) {
	dir := provision(t)
	i, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	path := filepath.Join(dir, "keeper.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	generation := strings.Repeat("a", 64)
	ended := make(chan error, 1)
	go func() {
		ended <- func() error {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			w := newWire(conn)
			hello, err := w.read()
			if err != nil {
				return err
			}
			p := hello.Proof
			p.ServerNonce = strings.Repeat("b", 64)
			p.Generation = generation
			p.KeeperBuild = p.ClientBuild
			secret, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			p.KeeperPublic = secret.PublicKey().Bytes()
			if err = w.write(message{Proof: p, Signature: sign(i.Keeper, "keeper", p)}); err != nil {
				return err
			}
			if _, err = w.read(); err != nil {
				return err
			}
			key, err := sessionKey(secret, p.ClientPublic, p)
			if err != nil {
				return err
			}
			inv := inventory{Proof: p}
			ack, err := json.Marshal(message{Proof: p, Signature: sign(i.Keeper, "inventory", inv), MAC: inventoryMAC(key, inv)})
			if err != nil {
				return err
			}
			frame := streamFrame{Sequence: 1, Body: json.RawMessage(`"read-ahead"`)}
			frame.MAC = streamMAC(key, "keeper", frame)
			data, err := json.Marshal(frame)
			if err != nil {
				return err
			}
			combined := append(append(append(ack, '\n'), data...), '\n')
			_, err = conn.Write(combined)
			return err
		}()
	}()
	err = WithSession(t.Context(), dir, authority.Epoch, generation, func(ch *Channel, _ Snapshot) error {
		var value string
		if err := ch.Receive(&value); err != nil {
			return err
		}
		if value != "read-ahead" {
			return errors.New("lost stream read-ahead")
		}
		return nil
	})
	if err != nil {
		t.Fatal("transition discarded authenticated buffered frame", err)
	}
	if err = <-ended; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReconciliationHoldsLockAndRechecksDrainEpoch(t *testing.T) {
	dir := provision(t)
	marker := errors.New("unproven orphan writers")
	if s, err := ListenWithRuntime(t.Context(), dir, RuntimeHooks{Prepare: func() error {
		if free, err := keeperAbsent(dir); err != nil || free {
			t.Error("orphan cleanup ran without singleton")
		}
		return marker
	}}); err == nil {
		_ = s.Close()
		t.Fatal("unproven orphan writers published empty custody")
	}
	if _, err := os.Lstat(filepath.Join(dir, "keeper.sock")); !os.IsNotExist(err) {
		t.Fatal("failed orphan cleanup published socket")
	}
	entered := make(chan struct{}, 1)
	resume := make(chan struct{})
	finished := make(chan bool, 1)
	s, err := ListenWithRuntime(t.Context(), dir, RuntimeHooks{Drain: func() (func(bool), error) {
		entered <- struct{}{}
		<-resume
		return func(committed bool) { finished <- committed }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- Drain(t.Context(), dir) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not enter runtime barrier")
	}
	if !s.mu.TryLock() {
		t.Fatal("runtime barrier nested under transport mutex")
	}
	s.mu.Unlock()
	_ = authority.Close()
	next, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	close(resume)
	select {
	case committed := <-finished:
		if committed {
			t.Fatal("stale epoch committed drain after runtime wait")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale drain did not release runtime barrier")
	}
	if err = <-ended; err == nil {
		t.Fatal("superseded drain succeeded")
	}
	if s.Drained() {
		t.Fatal("superseded drain stopped keeper")
	}
	if _, err = InspectSnapshot(t.Context(), dir, next.Epoch); err != nil {
		t.Fatal("stale drain wedged new authority", err)
	}
}

func TestRuntimeRetirementPrecedesSingletonRelease(t *testing.T) {
	dir := provision(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	server, err := ListenWithRuntime(t.Context(), dir, RuntimeHooks{Retire: func(context.Context) error { close(entered); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Close() }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime retirement did not start")
	}
	if absent, err := keeperAbsent(dir); err != nil || absent {
		t.Fatal("singleton released before physical cleanup", err)
	}
	close(release)
	if err = <-stopped; err != nil {
		t.Fatal(err)
	}
	if absent, err := keeperAbsent(dir); err != nil || !absent {
		t.Fatal("finished retirement leaked singleton", err)
	}
}

func TestFailedRuntimeRetirementRetainsCustodyUntilExit(t *testing.T) {
	dir := provision(t)
	server, err := ListenWithRuntime(t.Context(), dir, RuntimeHooks{Retire: func(context.Context) error { return ErrRefused }})
	if err != nil {
		t.Fatal(err)
	}
	// The real serveKeeper exits on cleanup failure. This in-process fixture
	// releases the descriptor only after observing its retained custody.
	defer func() { _ = server.lock.Close() }()
	if err = server.Close(); err == nil {
		t.Fatal("failed cleanup reported successful retirement")
	}
	if absent, err := keeperAbsent(dir); err != nil || absent {
		t.Fatal("failed cleanup published an empty successor", err)
	}
}
