// Package childlimits defines install-wide limits for sessions starting children.
package childlimits

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

const DefaultParallel = 3
const DefaultStartsPerHour = 120

type Limits struct {
	Parallel      int `json:"parallel"`
	StartsPerHour int `json:"starts_per_hour"`
}

var Ranges = map[string][2]int{"parallel": {1, 50}, "starts_per_hour": {1, 10000}}

func (l Limits) Validate() error {
	for _, f := range []struct {
		name  string
		value int
	}{{"parallel", l.Parallel}, {"starts_per_hour", l.StartsPerHour}} {
		r := Ranges[f.name]
		if f.value < r[0] || f.value > r[1] {
			return fmt.Errorf("%s must be between %d and %d", f.name, r[0], r[1])
		}
	}
	return nil
}

// FromEnv seeds defaults at service startup. Saved administrator settings win.
func FromEnv() Limits {
	read := func(name string, fallback, upper int) int {
		raw, set := os.LookupEnv(name)
		if !set {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > upper {
			log.Printf("%s must be a positive integer between 1 and %d; using default %d", name, upper, fallback)
			return fallback
		}
		return n
	}
	return Limits{read("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", DefaultParallel, 50), read("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", DefaultStartsPerHour, 10000)}
}
