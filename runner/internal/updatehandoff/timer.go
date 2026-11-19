package updatehandoff

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

var timerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,100}(\.service)?$`)

// TimerUnit extracts one explicitly named user timer from the observed full
// shell input. Dynamic names and multiple launches are deliberately unsupported.
// This names a candidate only: Begin/Notify still have to come from that unit,
// share installer ancestry, and install the exact binary consumed by Take.
func TimerUnit(command string) string {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}
	unit, count := "", 0
	syntax.Walk(f, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || call.Args[0].Lit() != "systemd-run" {
			return true
		}
		count++
		user, delayed, name := false, false, ""
		for i := 1; i < len(call.Args); i++ {
			arg := call.Args[i].Lit()
			switch {
			case arg == "--user":
				user = true
			case strings.HasPrefix(arg, "--on-active="):
				delayed = len(arg) > len("--on-active=")
			case strings.HasPrefix(arg, "--unit="):
				name = strings.TrimPrefix(arg, "--unit=")
			case arg == "--unit" && i+1 < len(call.Args):
				i++
				name = call.Args[i].Lit()
			case strings.HasPrefix(arg, "-"): // Other systemd-run options don't establish identity.
			default:
				i = len(call.Args) // Stop at the executable; its arguments aren't timer options.
			}
		}
		if user && delayed && timerName.MatchString(name) {
			unit = strings.TrimSuffix(name, ".service") + ".service"
		}
		return true
	})
	if count != 1 {
		return ""
	}
	return unit
}
