package daemon

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/persontime"
)

func TestPersonTimeRestartContinuation(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{})
	clock := &agent.PersonClock{}
	d.SetInputClock(clock)
	clock.Apply(agent.Input{PersonTime: &persontime.Account{Timezone: "America/New_York"}})
	d.EnqueueInput(agent.Input{Text: "metadata", Source: "system", PersonTime: &persontime.Account{Timezone: "America/New_York"}})
	<-d.queue
	if !d.continueAdopted("Continue after restart", "Resuming") {
		t.Fatal("restart did not enqueue")
	}
	in := <-d.queue
	if !strings.Contains(in.ModelText("test"), "America/New_York") {
		t.Fatal("restart continuation lost the person's clock")
	}
	fresh := clock.Apply(agent.Input{Text: "new server input", Source: "system", PersonTime: &persontime.Account{Timezone: "Europe/Paris"}})
	d.EnqueueInput(fresh)
	<-d.queue
	d.EnqueueInput(agent.Input{Text: "background finished", Source: "system"})
	if in := <-d.queue; !strings.Contains(in.ModelText("test"), "Europe/Paris") {
		t.Fatal("daemon-made input did not use latest account clock")
	}
}

func TestPersonTimeRestartClockRecord(t *testing.T) {
	dir := t.TempDir()
	driver := newCodexDriver(AgentDriverOpts{})
	sess := &agentSession{loop: driver, record: newRecordKeeper(dir, &atomic.Bool{}, AgentRecord{SessionID: "clock-restart", WorkDir: t.TempDir()})}
	sess.bindInputClock(driver, nil)
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	host.sessions["clock-restart"] = sess
	host.UserInput("clock-restart", agent.Input{Text: "Continue", Source: "chat", PersonTime: &persontime.Account{Timezone: "Europe/Paris"}})
	<-driver.queue
	saved := listAgentRecords(dir)
	if len(saved) != 1 || saved[0].PersonTime == nil {
		t.Fatal("server clock not retained in recovery record")
	}
	restarted := newCodexDriver(AgentDriverOpts{})
	restored := &agentSession{loop: restarted}
	restored.bindInputClock(restarted, saved[0].PersonTime)
	if !restarted.continueAdopted("Continue after daemon restart", "Resuming") {
		t.Fatal("restart did not continue")
	}
	in := <-restarted.queue
	if !strings.Contains(in.ModelText("test"), "Europe/Paris") {
		t.Fatal("restart path lost persisted account clock")
	}
}

func TestPersonTimeDaemonQueuesShareSessionClock(t *testing.T) {
	clock := &agent.PersonClock{}
	clock.Apply(agent.Input{PersonTime: &persontime.Account{Timezone: "America/New_York"}})
	cc := newClaudeCodeDriver(t.TempDir(), "", "", nil, nil)
	cc.SetInputClock(clock)
	cc.wakeForFinishedTask(agent.BackgroundTaskDonePayload{Description: "tests", Status: "completed"}, nil)
	if in := <-cc.queue; in.PersonTime == nil {
		t.Fatal("Claude background wake lost clock")
	}
	hermes := newHermesDriver(AgentDriverOpts{})
	hermes.SetInputClock(clock)
	if !hermes.continueAdopted("Continue", "Resuming") {
		t.Fatal("Hermes recovery did not enqueue")
	}
	if in := <-hermes.queue; in.PersonTime == nil {
		t.Fatal("Hermes recovery lost clock")
	}
	openclaw := newOpenclawDriver(AgentDriverOpts{})
	openclaw.SetInputClock(clock)
	openclaw.EnqueueInput(agent.Input{Text: "note reply", Source: "system"})
	if in := <-openclaw.queue; in.PersonTime == nil {
		t.Fatal("OpenClaw note reply lost clock")
	}
}
