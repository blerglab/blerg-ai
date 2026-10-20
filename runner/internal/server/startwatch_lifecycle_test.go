package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitStartWatch(t *testing.T, label string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not complete", label)
	}
}

func TestClusterWatcherCloseCancelsInflightPodPoll(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)
	life := newClusterWatchLifetime(t.Context())
	t.Cleanup(func() { unblock(); life.close() })
	h := NewHub()
	h.SetJobManager(&JobManager{BaseURL: srv.URL, Client: &http.Client{Timeout: time.Hour}, startWatches: life})
	h.starts.begin("fixture", clusterStartPlan("", nil))
	watchClusterStart(h, nil, "fixture", time.Hour)
	waitStartWatch(t, "pod poll entry", entered)
	joined := make(chan struct{})
	go func() { life.close(); close(joined) }()
	waitStartWatch(t, "pod request cancellation", cancelled)
	waitStartWatch(t, "pod watcher join", joined)
	life.close()
	if life.start() {
		t.Fatal("watcher started after lifetime closed")
	}
}

type heldPodTransport struct{ entered, cancelled, release chan struct{} }

func (r *heldPodTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	close(r.entered)
	select {
	case <-req.Context().Done():
		close(r.cancelled)
	case <-r.release:
	}
	<-r.release
	return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func TestClusterWatcherCloseJoinsBlockedPollCompletion(t *testing.T) {
	transport := &heldPodTransport{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(transport.release) }) }
	life := newClusterWatchLifetime(t.Context())
	t.Cleanup(func() { release(); life.close() })
	h := NewHub()
	h.SetJobManager(&JobManager{BaseURL: "http://fixture.invalid", Client: &http.Client{Transport: transport}, startWatches: life})
	h.starts.begin("fixture", clusterStartPlan("", nil))
	watchClusterStart(h, nil, "fixture", time.Hour)
	waitStartWatch(t, "held poll entry", transport.entered)
	joined := make(chan struct{})
	go func() { life.close(); close(joined) }()
	waitStartWatch(t, "held poll cancellation", transport.cancelled)
	select {
	case <-joined:
		t.Fatal("cleanup returned while watcher still owns its poll")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	waitStartWatch(t, "held poll completion", joined)
}

func TestClusterWatcherConcurrentStartAndClose(t *testing.T) {
	for range 100 {
		life := newClusterWatchLifetime(context.Background())
		var starts sync.WaitGroup
		starts.Add(1)
		go func() {
			defer starts.Done()
			if life.start() {
				defer life.done.Done()
				<-life.ctx.Done()
			}
		}()
		life.close()
		starts.Wait()
		life.close()
		if life.start() {
			t.Fatal("closed lifetime accepted a watcher")
		}
	}
}
