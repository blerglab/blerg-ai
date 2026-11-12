package daemon

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestVerbosityGuidanceAndUpdatedInput(t *testing.T) {
	for _, v := range []string{"narrated", "standard", "terse"} {
		paragraph := verbosityParagraph(v)
		if !strings.Contains(strings.ToLower(paragraph), "verbosity: "+v) || !strings.Contains(paragraph, "independently") {
			t.Fatalf("%s guidance: %s", v, paragraph)
		}
		for _, restrict := range []bool{false, true} {
			args := ccCommonArgs("", "", "", withSessionGuide(), func(o *ccOptions) { o.Verbosity = v; o.RestrictTools = restrict })
			if !strings.Contains(strings.Join(args, " "), paragraph) {
				t.Fatalf("Claude restrict=%v missing %s", restrict, v)
			}
		}
	}
	sess := &agentSession{}
	sess.verbosityUpdated.Store(true)
	sess.verbosity.Store(protocol.VerbosityTerse)
	in := sess.withVerbosity(agent.Input{Text: "Build it", ClientID: "input-1"})
	if !strings.Contains(in.Text, "Verbosity: Terse") || in.Payload().Text != "Build it" || in.ClientID != "input-1" {
		t.Fatalf("input %+v", in)
	}
	sess.verbosity.Store(protocol.VerbosityNarrated)
	in = sess.withVerbosity(agent.Input{Text: "Continue"})
	if !strings.Contains(in.Text, "Verbosity: Narrated") || in.Payload().Text != "Continue" {
		t.Fatalf("changed input %+v", in)
	}
}

func TestVerbosityEnvironmentIsNotInherited(t *testing.T) {
	t.Setenv(VerbosityEnvVar, "narrated")
	for _, entry := range buildSessionEnv(sessionEnvOpts{SessionID: "s1", Extra: map[string]string{VerbosityEnvVar: "terse"}}) {
		if strings.HasPrefix(entry, VerbosityEnvVar+"=") {
			t.Fatal("inherited verbosity environment")
		}
	}
	if validExtraEnvKey(VerbosityEnvVar) {
		t.Fatal("verbosity env accepted from caller")
	}
}

func TestVerbosityChangePersistsRecoveryRecord(t *testing.T) {
	dir := t.TempDir()
	frozen := &atomic.Bool{}
	sess := &agentSession{record: newRecordKeeper(dir, frozen, AgentRecord{SessionID: "session-1", Verbosity: "standard"})}
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	host.mu.Lock()
	host.sessions["session-1"] = sess
	host.mu.Unlock()
	host.SetVerbosity("session-1", "terse")
	if sess.verbosity.Load() != "terse" {
		t.Fatal("live style not updated")
	}
	records := listAgentRecords(dir)
	if len(records) != 1 || records[0].Verbosity != "terse" {
		t.Fatalf("recovery records: %+v", records)
	}
	host.SetVerbosity("session-1", "invalid")
	if sess.verbosity.Load() != "terse" {
		t.Fatal("invalid style accepted")
	}
}
