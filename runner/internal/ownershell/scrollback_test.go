package ownershell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestScrollbackRingKeepsTheNewestBytesInOrder(t *testing.T) {
	var r scrollback
	var all []byte
	for i := 0; len(all) < 3*ScrollbackBytes+123; i++ {
		chunk := []byte(fmt.Sprintf("line %07d\n", i))
		all = append(all, chunk...)
		r.write(chunk)
		if i%997 != 0 {
			continue
		}
		got, end := r.snapshot()
		if end != uint64(len(all)) {
			t.Fatalf("end %d after %d bytes", end, len(all))
		}
		want := all[max(0, len(all)-ScrollbackBytes):]
		if !bytes.Equal(got, want) {
			t.Fatalf("ring diverged after %d bytes", len(all))
		}
		if cap(r.buf) > ScrollbackBytes {
			t.Fatalf("ring grew to %d bytes", cap(r.buf))
		}
	}
	huge := bytes.Repeat([]byte("z"), ScrollbackBytes+5)
	huge[len(huge)-1] = 'E'
	r.write(huge)
	got, _ := r.snapshot()
	if len(got) != ScrollbackBytes || got[len(got)-1] != 'E' {
		t.Fatal("an oversized write did not leave its newest bytes")
	}
}

// fixtureFrames pushes output and waits until the drain has recorded it all.
func (f *fixture) emit(t *testing.T, h string, chunks ...[]byte) {
	t.Helper()
	want := uint64(0)
	f.m.mu.Lock()
	if s := f.m.shells[h]; s != nil && s.scrollback != nil {
		want = s.scrollback.end
	}
	f.m.mu.Unlock()
	for _, c := range chunks {
		f.s.outputs[0] <- c
		want += uint64(len(c))
	}
	eventually(t, "output was not recorded", func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		s := f.m.shells[h]
		return s != nil && s.scrollback != nil && s.scrollback.end == want
	})
}

// A refresh is a new attachment: it is shown what the shell printed before.
func TestReattachReplaysEarlierOutput(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	f.emit(t, i.Handle, []byte("$ ls\r\n"), []byte("README.md\r\n"))
	for range 2 {
		<-a.Output
	}
	f.m.Detach(f.actor, i.Handle, a.ID) // the page went away
	again, err := f.m.AttachWithReplay(t.Context(), f.actor, i.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Replay) != "$ ls\r\nREADME.md\r\n" || again.ReplayEnd != uint64(len(again.Replay)) {
		t.Fatalf("replay %q end %d", again.Replay, again.ReplayEnd)
	}
	plain, err := f.m.Attach(t.Context(), f.actor, i.Handle, true)
	if err != nil || plain.Replay != nil {
		t.Fatal("an attachment that did not ask for replay received it")
	}
}

// Output produced while nobody is attached is kept, and still returns its
// credit at once, so a closed tab never blocks the program.
func TestDetachedOutputIsKeptAndCredited(t *testing.T) {
	f, _ := setupClock(t)
	credit := &creditSupervisor{fakeSupervisor: f.s}
	f.m.supervisor = credit
	i, a := f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	var produced []byte
	for n := range 20 {
		chunk := []byte(fmt.Sprintf("while away %02d\n", n))
		produced = append(produced, chunk...)
		f.emit(t, i.Handle, chunk)
	}
	if credit.total() != len(produced) {
		t.Fatalf("credited %d of %d detached bytes", credit.total(), len(produced))
	}
	back, err := f.m.AttachWithReplay(t.Context(), f.actor, i.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Replay, produced) {
		t.Fatal("detached output missing from the replay")
	}
}

// More than the ring holds: the oldest output is evicted, the newest kept.
func TestScrollbackEvictsOldestOutput(t *testing.T) {
	f, clock := setupClock(t)
	i, a := f.open(t)
	f.m.Detach(f.actor, i.Handle, a.ID)
	var produced []byte
	for n := 0; len(produced) < ScrollbackBytes+3*MaxFrame; n++ {
		chunk := bytes.Repeat([]byte{byte('a' + n%26)}, MaxFrame)
		produced = append(produced, chunk...)
		clock.Add(time.Second) // keep within the per-second output budget
		f.emit(t, i.Handle, chunk)
	}
	back, err := f.m.AttachWithReplay(t.Context(), f.actor, i.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Replay, produced[len(produced)-ScrollbackBytes:]) || back.ReplayEnd != uint64(len(produced)) {
		t.Fatal("ring did not keep exactly the newest bytes")
	}
}

// Reattaching while output streams: replay followed by the new attachment's
// output is the stream from the replay's start, with no gap and no repeat.
func TestReplayBoundaryHasNoGapOrDuplicate(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	const frames = 120 // under the per-second frame budget
	frame := func(n int) []byte { return []byte(fmt.Sprintf("<%03d>", n)) }
	go func() {
		for n := range frames {
			f.s.outputs[0] <- frame(n)
		}
	}()
	// The first view reads a while, then a refresh attaches mid-stream.
	for range 30 {
		<-a.Output
	}
	back, err := f.m.AttachWithReplay(t.Context(), f.actor, i.Handle, true)
	if err != nil {
		t.Fatal(err)
	}
	got := append([]byte(nil), back.Replay...)
	if len(back.Replay) == 0 {
		t.Fatal("the reattach did not land mid-stream")
	}
	live := 0
	deadline := time.After(5 * time.Second)
	var stream []byte
	for n := range frames {
		stream = append(stream, frame(n)...)
	}
	for !bytes.HasSuffix(got, frame(frames-1)) {
		select {
		case data, ok := <-back.Output:
			if !ok {
				t.Fatal("attachment closed")
			}
			got = append(got, data...)
			live += len(data)
		case <-deadline:
			t.Fatalf("stream incomplete: %q", got)
		}
	}
	if live == 0 {
		t.Fatal("no live output followed the replay")
	}
	start := uint64(len(stream)) - uint64(len(got))
	if back.ReplayEnd-uint64(len(back.Replay)) != start || !bytes.Equal(got, stream[start:]) {
		t.Fatalf("replay plus live output is not the exact stream suffix: %q", got)
	}
}

// Replay has Attach's authorization: another login or account gets nothing.
func TestScrollbackIsReadableOnlyByTheShellsLogin(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	f.emit(t, i.Handle, []byte("private-output"))
	<-a.Output
	for _, other := range []Actor{{"human", "owner", "other-login"}, {"human", "other-owner", "login"}} {
		if back, err := f.m.AttachWithReplay(t.Context(), other, i.Handle, true); err == nil || back.Replay != nil {
			t.Fatalf("%+v read the shell's scrollback", other)
		}
	}
}

// Ending the shell frees its scrollback; nothing outlives the shell.
func TestScrollbackFreedWhenTheShellEnds(t *testing.T) {
	f, _ := setupClock(t)
	i, a := f.open(t)
	f.emit(t, i.Handle, []byte("before end"))
	<-a.Output
	f.m.mu.Lock()
	s := f.m.shells[i.Handle]
	f.m.mu.Unlock()
	if err := f.m.Close(t.Context(), f.actor, i.Handle); err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	freed := s.scrollback == nil
	f.m.mu.Unlock()
	if !freed {
		t.Fatal("scrollback outlived its shell")
	}
	if _, err := f.m.AttachWithReplay(context.Background(), f.actor, i.Handle, true); !errors.Is(err, ErrDenied) {
		t.Fatal("ended shell still attachable", err)
	}
}
