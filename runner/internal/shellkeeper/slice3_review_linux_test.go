//go:build linux

package shellkeeper

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestServiceChannelChecksDurableEpochOnEveryUse(t *testing.T) {
	dir := provision(t)
	channel := make(chan *Channel, 1)
	release := make(chan struct{})
	server, err := ListenService(t.Context(), dir, nil, func(_ context.Context, ch *Channel) error { channel <- ch; <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = server.Close() }()
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	hold := make(chan struct{})
	go func() {
		ended <- WithSession(t.Context(), dir, authority.Epoch, server.generation, func(*Channel, Snapshot) error { <-hold; return nil })
	}()
	defer func() { close(hold); <-ended }()
	var ch *Channel
	select {
	case ch = <-channel:
	case <-time.After(3 * time.Second):
		t.Fatal("channel unavailable")
	}
	if !ch.Current() {
		t.Fatal("current durable authority refused")
	}
	_ = authority.Close()
	next, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	if ch.Current() {
		t.Fatal("old stream survived signed durable epoch advancement")
	}
}
func TestChannelSendDeadlineIncludesWriterQueue(t *testing.T) {
	server, _ := localPair(t)
	ch := newChannel(server, make([]byte, 32), "client", "keeper", nil)
	ch.write <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- ch.SendContext(ctx, "blocked") }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled sender wrote")
		}
	case <-time.After(time.Second):
		<-ch.write
		t.Fatal("deadline did not include writer queue")
	}
	<-ch.write
}

func TestEmptyLaunchMetadataInventoryRemainsDistinctAndDoesNotFenceDrain(t *testing.T) {
	dir := provision(t)
	var candidates []Candidate
	for i := 0; i < 128; i++ {
		candidates = append(candidates, EmptyLaunchCandidate(fmt.Sprintf("session-%03d", i)))
	}
	server, err := ListenService(t.Context(), dir, func() []Candidate { return candidates }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil || len(view.Candidates) != 128 {
		t.Fatal("empty launches disappeared or collided", err)
	}
	_ = authority.Close()
	if err = CheckUpdate(t.Context(), dir); err != nil {
		t.Fatal("empty launch metadata fenced update", err)
	}
	if err = Drain(t.Context(), dir); err != nil {
		t.Fatal("empty launch metadata fenced drain", err)
	}
}
