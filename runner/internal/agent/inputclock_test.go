package agent

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/persontime"
)

func TestInputClockNativeAndReplay(t *testing.T) {
	clock := &PersonClock{}
	clock.Apply(Input{PersonTime: &persontime.Account{Timezone: "Europe/Paris"}})
	loop := &Loop{wake: make(chan struct{}, 1)}
	loop.SetInputClock(clock)
	loop.EnqueueInput(Input{Text: "wake", Source: "system"})
	if loop.queue[0].PersonTime == nil || loop.queue[0].PersonTime.Timezone != "Europe/Paris" {
		t.Fatal("native input lost session clock")
	}
	// Replaying an older input may retain its metadata, but must not replace the latest server clock.
	loop.EnqueueInput(Input{Text: "replay", PersonTime: &persontime.Account{Timezone: "UTC"}})
	loop.EnqueueInput(Input{Text: "next wake", Source: "system"})
	if loop.queue[2].PersonTime.Timezone != "Europe/Paris" {
		t.Fatal("replay replaced latest server clock")
	}
}
