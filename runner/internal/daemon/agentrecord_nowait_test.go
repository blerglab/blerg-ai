package daemon

import "testing"

// `blerg-runner ask --no-wait` and `ask --resolve` return at once: a restart during one is not a
// session waiting on the person. A blocking ask is.
func TestNoWaitAskCommandDoesNotWaitOnThePerson(t *testing.T) {
	for cmd, waits := range map[string]bool{
		`blerg-runner ask "Ship it?"`:                    true,
		`blerg-runner ask --timeout 60 "Ship it?"`:       true,
		`blerg-runner ask --no-wait "Ship it?"`:          false,
		`blerg-runner ask "Ship it?" --no-wait`:          false,
		`blerg-runner ask --resolve q-1`:                 false,
		`cd x && blerg-runner ask --no-wait "a" && make`: false,
	} {
		if got := (OpenCall{Tool: "Bash", Detail: cmd}).asksPerson(); got != waits {
			t.Errorf("%s: asksPerson = %v, want %v", cmd, got, waits)
		}
	}
}
