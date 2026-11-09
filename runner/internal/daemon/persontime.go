package daemon

import (
	"reflect"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/persontime"
)

func (sess *agentSession) bindInputClock(driver sessionDriver, saved *persontime.Account) {
	if clocked, ok := driver.(interface{ SetInputClock(*agent.PersonClock) }); ok {
		clocked.SetInputClock(&sess.inputClock)
	}
	sess.inputClock.OnChange = func(clock *persontime.Account) {
		if sess.record != nil {
			sess.record.update(func(r *AgentRecord) bool {
				if reflect.DeepEqual(r.PersonTime, clock) {
					return false
				}
				r.PersonTime = clock
				return true
			})
		}
	}
	sess.inputClock.Apply(agent.Input{PersonTime: saved})
}
