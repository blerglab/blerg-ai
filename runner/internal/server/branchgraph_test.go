package server

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBranchGraphHTTPAuthorizationAndBounds(t *testing.T) {
	fx := newCommitFx(t)
	path := "/api/sessions/" + fx.sess + "/git/graph"
	owner := fx.token(topicOwner, "human")
	for _, token := range []string{fx.token(topicOther, "human"), fx.token(topicOwner, "agent")} {
		if res := fx.refused(path, token); res.Code != http.StatusForbidden {
			t.Fatalf("foreign: %d", res.Code)
		}
	}
	if res := fx.refused(path+"?depth=201", owner); res.Code != 422 {
		t.Fatalf("depth: %d", res.Code)
	}
	res := fx.answered(path+"?depth=100", owner, func(q protocol.ReadCommit) protocol.CommitResult {
		if !q.Graph || q.Depth != 100 || q.Branch != "" {
			t.Errorf("request: %+v", q)
		}
		return protocol.CommitResult{OK: true, Graph: &protocol.BranchGraph{Refs: []protocol.GraphRef{{Name: "wip/example", SHA: fixtureSHA}}, Commits: []protocol.GraphCommit{{SHA: fixtureSHA, Parents: []string{}, Subject: "Work"}}}}
	})
	if res.Code != 200 {
		t.Fatalf("graph: %d %s", res.Code, res.Body)
	}
	if res.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cache")
	}
	res = fx.answered(path, owner, func(q protocol.ReadCommit) protocol.CommitResult {
		return protocol.CommitResult{OK: true, Graph: &protocol.BranchGraph{Commits: []protocol.GraphCommit{{SHA: "invalid"}}}}
	})
	if res.Code != 409 {
		t.Fatalf("invalid reply: %d", res.Code)
	}
}

func TestBranchGraphReplyLimits(t *testing.T) {
	graph := &protocol.BranchGraph{Refs: []protocol.GraphRef{{Name: strings.Repeat("a", 1024), SHA: fixtureSHA}}}
	if !validBranchGraph(graph, 50) {
		t.Fatal("bounded ref refused")
	}
	graph.Refs[0].Name += "x"
	if validBranchGraph(graph, 50) {
		t.Fatal("oversized ref accepted")
	}
	graph.Refs = nil
	graph.Commits = make([]protocol.GraphCommit, 51)
	if validBranchGraph(graph, 50) {
		t.Fatal("oversized history accepted")
	}
}

func TestBranchGraphLeavesCapacityForPlanSync(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "desktop", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	go func() {
		_, err := h.requestCommitRead(ctx, dc, "owner", protocol.ReadCommit{SessionID: "session", Graph: true, Depth: 50}, time.Second*10)
		done <- err
	}()
	<-dc.send
	if _, err := h.requestCommitRead(ctx, dc, "owner", protocol.ReadCommit{SessionID: "session", Graph: true, Depth: 50}, time.Second); !errors.Is(err, errCommitBusy) {
		t.Fatalf("second graph: %v", err)
	}
	other := &DaemonConn{ID: "other-desktop", send: make(chan []byte, 8)}
	h.Register(other)
	h.SetSessionOwner("other-session", other.ID)
	if _, err := h.requestCommitRead(ctx, other, "owner", protocol.ReadCommit{SessionID: "other-session", Graph: true, Depth: 50}, time.Second); !errors.Is(err, errCommitBusy) {
		t.Fatalf("same person's graph on another desktop: %v", err)
	}
	go func() {
		_, err := h.requestCommitRead(ctx, dc, "owner", protocol.ReadCommit{SessionID: "session", MergedChild: "child"}, time.Second*10)
		done <- err
	}()
	select {
	case <-dc.send:
	case <-time.After(time.Second):
		t.Fatal("graph starved merged-child read")
	}
	cancel()
	<-done
	<-done
}

func TestBranchGraphTagReplyLimits(t *testing.T) {
	graph := &protocol.BranchGraph{Commits: []protocol.GraphCommit{{SHA: fixtureSHA}}, Tags: []protocol.GraphTag{{GraphRef: protocol.GraphRef{Name: "v1", SHA: fixtureSHA}, Contains: []string{fixtureSHA}}}}
	if !validBranchGraph(graph, 50) {
		t.Fatal("valid tag refused")
	}
	graph.Tags[0].Contains = append(graph.Tags[0].Contains, fixtureSHA)
	if validBranchGraph(graph, 50) {
		t.Fatal("duplicate ancestry accepted")
	}
	graph.Tags[0].Contains = []string{strings.Repeat("b", 40)}
	if validBranchGraph(graph, 50) {
		t.Fatal("ancestry outside displayed window accepted")
	}
	graph.Tags[0].Contains = nil
	graph.Tags[0].Previous = "missing"
	if validBranchGraph(graph, 50) {
		t.Fatal("unknown previous tag accepted")
	}
	graph.Tags = make([]protocol.GraphTag, 17)
	if validBranchGraph(graph, 50) {
		t.Fatal("unbounded tags accepted")
	}
}
