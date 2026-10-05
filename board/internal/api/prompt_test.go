package api

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// allPrompts renders every prompt role for a using board/card fixtures,
// failing the test on any render error. buildReviewPrompt picks its brief
// from the card, so it is called twice: once for a PR card (reviewer.md) and
// once for a body-only spec card (specreview.md).
func allPrompts(t *testing.T, a *API) []string {
	t.Helper()
	board := db.Board{Name: "test-board"}
	card := db.Card{Number: 1, Title: "a card"}
	specCard := db.Card{Number: 1, Title: "a card", Body: strPtrOf("a spec worth reviewing")}

	worker, err := a.buildCardPrompt(board, card, "")
	if err != nil {
		t.Fatalf("buildCardPrompt: %v", err)
	}
	discuss, err := a.buildDiscussPrompt(board, card, "")
	if err != nil {
		t.Fatalf("buildDiscussPrompt: %v", err)
	}
	reviewer, err := a.buildReviewPrompt(context.Background(), board, card)
	if err != nil {
		t.Fatalf("buildReviewPrompt: %v", err)
	}
	specReviewer, err := a.buildReviewPrompt(context.Background(), board, specCard)
	if err != nil {
		t.Fatalf("buildReviewPrompt(spec card): %v", err)
	}
	boardSession, err := a.buildBoardSessionPrompt(board, "", "")
	if err != nil {
		t.Fatalf("buildBoardSessionPrompt: %v", err)
	}
	bootstrap, err := a.buildBootstrapPrompt(board, "")
	if err != nil {
		t.Fatalf("buildBootstrapPrompt: %v", err)
	}
	return []string{worker, discuss, reviewer, specReviewer, boardSession, bootstrap}
}

func TestPromptBuildersOmitInfraParagraphWithoutConfig(t *testing.T) {
	a := New(nil, nil, nil)
	for i, p := range allPrompts(t, a) {
		if strings.Contains(p, "Infrastructure reference") {
			t.Errorf("prompt %d: unexpected infra paragraph with no runner config:\n%s", i, p)
		}
		if strings.Contains(p, ".svc.cluster") || strings.Contains(p, "homelab") {
			t.Errorf("prompt %d: leaked infra-specific text with no runner config:\n%s", i, p)
		}
	}
}

func TestPromptBuildersIncludeInfraParagraphWhenConfigured(t *testing.T) {
	a := New(nil, nil, nil)
	a.SetRunner(RunnerConfig{InfraDocsURL: "https://example.com/agents", InfraDocsNote: "a test environment"})
	for i, p := range allPrompts(t, a) {
		if !strings.Contains(p, "https://example.com/agents") {
			t.Errorf("prompt %d: missing configured infra URL:\n%s", i, p)
		}
	}
}

// The board brief carries the snapshot verbatim and points the session at it;
// with no snapshot (a state read that failed, or a role that has none) the
// brief must not grow a dangling reference to a block that isn't there.
func TestBoardSessionPromptCarriesSnapshot(t *testing.T) {
	a := New(nil, nil, nil)
	board := db.Board{Name: "test-board"}
	snap := renderBoardSnapshot(snapshotFixture(2, 1))

	with, err := a.buildBoardSessionPrompt(board, snap, "")
	if err != nil {
		t.Fatalf("buildBoardSessionPrompt: %v", err)
	}
	if !strings.Contains(with, snap) {
		t.Errorf("board brief dropped the snapshot:\n%s", with)
	}
	if !strings.Contains(with, "re-read any card you are about to change") {
		t.Errorf("board brief does not tell the session to re-read before mutating:\n%s", with)
	}
	if !strings.Contains(with, "2026-08-06T09:12:31Z") {
		t.Errorf("board brief does not name the snapshot timestamp:\n%s", with)
	}

	without, err := a.buildBoardSessionPrompt(board, "", "")
	if err != nil {
		t.Fatalf("buildBoardSessionPrompt: %v", err)
	}
	if strings.Contains(without, "one-line-per-card index") || strings.Contains(without, "Board snapshot") {
		t.Errorf("board brief references a snapshot it was not given:\n%s", without)
	}
}

func TestBootstrapPromptGitCredential(t *testing.T) {
	board := db.Board{Name: "test-board"}

	a := New(nil, nil, nil)
	withoutCred, err := a.buildBootstrapPrompt(board, "")
	if err != nil {
		t.Fatalf("buildBootstrapPrompt: %v", err)
	}
	if strings.Contains(withoutCred, "BLERG_RUNNER_GIT_TOKEN") {
		t.Errorf("no-cfg bootstrap prompt still names BLERG_RUNNER_GIT_TOKEN:\n%s", withoutCred)
	}
	if !strings.Contains(withoutCred, "ask the human") {
		t.Errorf("no-cred bootstrap prompt should ask the human for a credential:\n%s", withoutCred)
	}

	a2 := New(nil, nil, nil)
	a2.SetRunner(RunnerConfig{GitCredentialEnv: "MY_GIT_TOKEN"})
	withCred, err := a2.buildBootstrapPrompt(board, "")
	if err != nil {
		t.Fatalf("buildBootstrapPrompt: %v", err)
	}
	if !strings.Contains(withCred, "MY_GIT_TOKEN") {
		t.Errorf("bootstrap prompt missing configured GitCredentialEnv:\n%s", withCred)
	}
	if strings.Contains(withCred, "BLERG_RUNNER_GIT_TOKEN") {
		t.Errorf("bootstrap prompt leaked hardcoded BLERG_RUNNER_GIT_TOKEN:\n%s", withCred)
	}
}
