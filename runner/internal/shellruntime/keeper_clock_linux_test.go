//go:build linux

package shellruntime

import (
	"errors"
	"testing"
	"time"
)

func TestRestartOnlyClockRefusesLapseRollbackAndClockDisagreement(t *testing.T) {
	for _, name := range []string{"healthy", "sampling_lapse", "sleep_wall_only", "wall_back", "elapsed_back", "outage"} {
		t.Run(name, func(t *testing.T) {
			wall := time.Unix(10000, 0)
			elapsed := time.Hour
			var failure error
			clock, err := restartOnlyClock(func() (time.Time, time.Duration, error) { return wall, elapsed, failure })
			if err != nil {
				t.Fatal(err)
			}
			wall = wall.Add(time.Second)
			elapsed += time.Second
			switch name {
			case "sampling_lapse":
				wall = wall.Add(time.Minute)
				elapsed += time.Minute
			case "sleep_wall_only":
				wall = wall.Add(time.Hour)
			case "wall_back":
				wall = wall.Add(-2 * time.Second)
			case "elapsed_back":
				elapsed -= 2 * time.Second
			case "outage":
				failure = errors.New("clock unavailable")
			}
			_, _, err = clock()
			if (err == nil) != (name == "healthy") {
				t.Fatal("restart clock accepted unproved interval", err)
			}
			if name != "healthy" {
				failure = nil
				wall = wall.Add(time.Second)
				elapsed += time.Second
				if _, _, err = clock(); err == nil {
					t.Fatal("clock refusal was not latched")
				}
			}
		})
	}
	if verifiedPersistentKernel("6.8-microsoft-standard-WSL2") {
		t.Fatal("restart profile widened the WSL sleep boundary")
	}
	wall := time.Now()
	elapsed := time.Hour
	read := func() (time.Time, time.Duration, error) { return wall, elapsed, nil }
	clock, err := keeperClockForKernel("6.8-microsoft-standard-WSL2", read)
	if err != nil {
		t.Fatal("WSL restart profile refused healthy interval", err)
	}
	stamp, _, err := clock()
	if err != nil || stamp != stamp.Round(0) {
		t.Fatal("restart clock used Go monotonic instead of actual wall time", err)
	}
	wall = wall.Add(time.Minute)
	if _, _, err = clock(); err == nil {
		t.Fatal("WSL restart profile survived unproved sleep")
	}
	if _, err = keeperClockForKernel("linuxkit", read); err == nil {
		t.Fatal("restart profile widened Docker Desktop clock trust")
	}
}
