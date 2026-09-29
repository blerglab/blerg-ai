package runner

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type fakeSender struct {
	sent []any
	err  error
	// failFirst makes the first n sends fail (a connection not up yet).
	failFirst int
}

func (f *fakeSender) Send(msg any) error {
	if f.err != nil {
		return f.err
	}
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("daemon: not connected to server")
	}
	f.sent = append(f.sent, msg)
	return nil
}

func TestClassifyCloneError(t *testing.T) {
	const token = "ghp_SECRETSECRET"
	cases := []struct {
		out, want string
	}{
		{"fatal: repository 'https://x-access-token:" + token + "@github.com/org/nope.git/' not found", "not found"},
		{"remote: Repository not found.", "not found"},
		{"fatal: Authentication failed for 'https://github.com/org/repo.git/'", "authentication failed"},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", "authentication failed"},
		{"fatal: unable to access 'https://github.com/org/repo.git/': The requested URL returned error: 403", "authentication failed"},
		{"fatal: unable to access 'https://github.com/org/repo.git/': Could not resolve host: github.com", "reach the git host"},
		{"error: something odd happened", "git clone failed"},
		{"exec: \"git\": executable file not found in $PATH", "git is not available"},
		{"sh: git: command not found", "git is not available"},
		{"warning: Could not find remote branch wip/x to clone.\nfatal: Remote branch wip/x not found in upstream origin", "branch"},
		{"fatal: couldn't find remote ref refs/heads/nope", "branch"},
	}
	for _, tc := range cases {
		detail, hint := classifyCloneError("clone: exit status 128: " + tc.out)
		if !strings.Contains(strings.ToLower(detail), strings.ToLower(tc.want)) {
			t.Errorf("classify(%q) = %q, want ~%q", tc.out, detail, tc.want)
		}
		if hint == "" {
			t.Errorf("classify(%q): no next step", tc.out)
		}
		// git's text (and the token-bearing URL in it) never passes through.
		if strings.Contains(detail+hint, token) || strings.Contains(detail+hint, "x-access-token") {
			t.Errorf("classify(%q) leaked the clone URL: %q / %q", tc.out, detail, hint)
		}
	}
}

func TestMissingEngineCredential(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	cases := []struct {
		engine  string
		vals    map[string]string
		missing bool
	}{
		{"", nil, true},
		{"claude", map[string]string{"ANTHROPIC_API_KEY": "k"}, false},
		{"", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "t"}, false},
		{"codex", map[string]string{"ANTHROPIC_API_KEY": "k"}, true},
		{"codex", map[string]string{"CODEX_AUTH_JSON": "{}"}, false},
		{"hermes", nil, true},
		{"hermes", map[string]string{"HERMES_ENV_CONTENTS": "X=1"}, false},
		{"openclaw", nil, false}, // not a cluster engine; nothing to check here
	}
	for _, tc := range cases {
		detail, hint := missingEngineCredential(tc.engine, env(tc.vals))
		if (detail != "") != tc.missing {
			t.Errorf("engine %q with %v: detail = %q, want missing=%v", tc.engine, tc.vals, detail, tc.missing)
		}
		if tc.missing && hint == "" {
			t.Errorf("engine %q: no next step", tc.engine)
		}
	}
}

func TestStageReporterSendsStartStageEvents(t *testing.T) {
	fs := &fakeSender{}
	r := &stageReporter{s: fs, sessionID: "sid"}
	r.report(done(protocol.StageConnect), active(protocol.StageClone, "Cloning org/repo"))
	if len(fs.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fs.sent))
	}
	ev, ok := fs.sent[0].(protocol.AgentEvent)
	if !ok || ev.Kind != protocol.StartStageKind || ev.SessionID != "sid" || ev.Transient || len(ev.ClientEventID) != 36 {
		t.Fatalf("event = %+v", fs.sent[0])
	}
	var p protocol.StartStagePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Plan || len(p.Stages) != 2 || p.Stages[1].State != protocol.StageStateActive {
		t.Fatalf("payload = %s", ev.Payload)
	}
}

func TestStageReporterFailEndsAFreshStartButNotAResume(t *testing.T) {
	fs := &fakeSender{}
	(&stageReporter{s: fs, sessionID: "sid"}).fail(protocol.StageClone, "Git authentication failed", "fix it", false)
	if len(fs.sent) != 2 {
		t.Fatalf("fresh start: sent %d, want the stage and the error status", len(fs.sent))
	}
	st, ok := fs.sent[1].(protocol.SessionStateChanged)
	if !ok || st.Status != "error" || st.Message == nil || *st.Message != "Git authentication failed" {
		t.Fatalf("status = %+v", fs.sent[1])
	}

	fs = &fakeSender{}
	(&stageReporter{s: fs, sessionID: "sid"}).fail(protocol.StageClone, "Git authentication failed", "fix it", true)
	if len(fs.sent) != 1 {
		t.Fatalf("resume: sent %d, want only the stage (the session stays resumable)", len(fs.sent))
	}

	// Never connected: gives up after finalWait, never a panic.
	r := &stageReporter{s: &fakeSender{err: errors.New("not connected")}, sessionID: "sid",
		finalWait: 20 * time.Millisecond, retryEvery: 5 * time.Millisecond}
	r.fail(protocol.StageClone, "x", "y", false)
}

// Reports made before the connection is up are kept, in order, and the
// failure report is retried until it gets through — it is the one the user
// needs, and the pod exits right after.
func TestStageReporterQueuesUntilConnectedAndRetriesTheFailure(t *testing.T) {
	fs := &fakeSender{failFirst: 3}
	r := &stageReporter{s: fs, sessionID: "sid", finalWait: 2 * time.Second, retryEvery: time.Millisecond}
	r.report(done(protocol.StageConnect), active(protocol.StageClone, "Cloning"))
	if len(fs.sent) != 0 {
		t.Fatalf("sent %d while disconnected", len(fs.sent))
	}
	r.fail(protocol.StageClone, "Repository not found, or the git credential can't see it", "check", false)
	if len(fs.sent) != 3 {
		t.Fatalf("delivered %d, want the queued report, the failure and the status", len(fs.sent))
	}
	if _, ok := fs.sent[0].(protocol.AgentEvent); !ok {
		t.Fatalf("first delivered = %T, want the earliest report", fs.sent[0])
	}
	if _, ok := fs.sent[2].(protocol.SessionStateChanged); !ok {
		t.Fatalf("last delivered = %T, want the error status", fs.sent[2])
	}
}

func TestClassifyWorkspaceErrorOnlyBlamesCloneForCloneFailures(t *testing.T) {
	if d, _ := classifyWorkspaceError(errors.New("mkdir /workspace: permission denied")); d != "Preparing the workspace failed" {
		t.Errorf("non-clone error classified as %q", d)
	}
	if d, _ := classifyWorkspaceError(&cloneError{out: "remote: Repository not found."}); !strings.Contains(d, "not found") {
		t.Errorf("clone error classified as %q", d)
	}
}
