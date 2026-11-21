package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestResolveWorkerKind(t *testing.T) {
	for _, tc := range []struct{ kind, title, want string }{
		{"role", "BLG-499 Gates", "role"}, {"review", "Review", "review"}, {"research", "Find", "research"},
		{"", "BLG-123 Fix", "card"}, {"", "Fix #123", "card"}, {"", "board:abc#123 Fix", "card"},
		{"", "Reviewer research role", ""}, {"", "hello", ""},
	} {
		got, err := resolveWorkerKind(tc.kind, tc.title)
		if err != nil || got != tc.want {
			t.Errorf("%q %q = %q, %v; want %q", tc.kind, tc.title, got, err, tc.want)
		}
	}
	if _, err := resolveWorkerKind("other", ""); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestWorkerKindStoredAtStart(t *testing.T) {
	fx := newMCPFx(t)
	for _, kind := range []string{"card", "role", "review", "research", ""} {
		result, problem := fx.api.StartSession(t.Context(), fx.owner(), RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", WorkerKind: kind, Title: "Worker"}, "")
		if problem != nil {
			t.Fatal(problem)
		}
		recvSpawn(t, fx.dc)
		row, err := db.GetSession(t.Context(), fx.pool, result.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if row.WorkerKind != kind {
			t.Fatalf("stored kind %q, want %q", row.WorkerKind, kind)
		}
		info := sessionRowToInfo(*row, "")
		if info.WorkerKind != kind {
			t.Fatalf("wire kind %q, want %q", info.WorkerKind, kind)
		}
	}
}

func TestChildWorkerKindContract(t *testing.T) {
	fx := newDelegFx(t)
	fx.rule("codex", runnerRuntimeDocker, false)
	for i, tc := range []struct{ kind, title, want string }{
		{"card", "Fix", "card"}, {"role", "BLG-123 Gates", "role"}, {"review", "Review", "review"}, {"research", "Investigate", "research"},
		{"", "BLG-123 Fix", "card"}, {"", "Role review research", ""}, {"", "", ""},
	} {
		fx := newDelegFx(t)
		fx.rule("codex", runnerRuntimeDocker, false)
		body := codexBody()
		body["title"] = tc.title
		body["prompt"] = "BLG-123 in the prompt is not a title"
		if tc.kind != "" {
			body["kind"] = tc.kind
		}
		code, out, _ := fx.postChild(fx.parent, fx.parentTok, body, fmt.Sprintf("kind-%d", i))
		if code != http.StatusCreated {
			t.Fatalf("start: %d %v", code, out)
		}
		spawn := fx.spawn()
		row, err := db.GetSession(t.Context(), fx.pool, spawn.SessionID)
		if err != nil || row.WorkerKind != tc.want {
			t.Fatalf("stored: %+v %v; want %q", row, err, tc.want)
		}
	}
	body := codexBody()
	body["kind"] = "invalid"
	code, _, _ := fx.postChild(fx.parent, fx.parentTok, body, "bad-kind")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid kind status %d", code)
	}
}

func TestChildWorkerKindInferenceKeepsLegacyRetryHash(t *testing.T) {
	cs, _, problem := parseChildStart([]byte(`{"title":"BLG-123 Fix","prompt":"p","no_repo":true}`), childHost{cluster: true})
	if problem != nil {
		t.Fatal(problem)
	}
	legacy := cs
	legacy.WorkerKind = ""
	if childRequestHash(cs) != childRequestHash(legacy) {
		t.Fatal("inference changed an omitted kind's stored request hash")
	}
	req := childRunnerRequest(&db.SessionRow{ID: "parent"}, cs, "rule:test")
	legacyReq := req
	legacyReq.WorkerKind = ""
	got, err := startRequestHash(req)
	want, _ := startRequestHash(legacyReq)
	if err != nil || got != want {
		t.Fatal("inference changed an omitted kind's start hash", err)
	}
}
