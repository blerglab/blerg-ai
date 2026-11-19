//go:build linux

package clusterworkspace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Release a deadline on an observed transport event, without a wall-clock sleep.
type cloneDeadlineContext struct{ context.Context }

// Hide the embedded cancellation state so children observe this context's Err.
func (c cloneDeadlineContext) Value(any) any { return nil }

func (c cloneDeadlineContext) Err() error {
	if c.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

func TestCloneTransportDeadlineCause(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	dir := t.TempDir()
	err := gitCall(cloneDeadlineContext{ctx}, dir, Member{CloneURL: server.URL + "/repo.git"}, "clone", "--", server.URL+"/repo.git", dir+"/repo")
	cancel()
	<-done
	if CloneCause(err) != "deadline" {
		t.Fatalf("transport deadline lost: %v", err)
	}
}
