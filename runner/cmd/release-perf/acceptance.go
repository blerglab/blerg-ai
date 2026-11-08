package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Optional diagnostics run once while this command still owns the isolated
// services. Calibration repeats must never each start another sustained soak.
func acceptanceEnv(run int, duration time.Duration, daemonPID int, pids map[string]int, output string) (map[string]string, error) {
	if duration < 0 || (duration > 0 && daemonPID <= 0) {
		return nil, errors.New("acceptance requires a nonnegative duration and an explicit daemon PID for a soak")
	}
	if duration == 0 || run != 0 {
		return map[string]string{}, nil
	}
	processes := make(map[string]int, len(pids)+1)
	for name, pid := range pids {
		processes[name] = pid
	}
	processes["daemon"] = daemonPID
	encoded, err := json.Marshal(processes)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"RELEASE_PERF_SOAK_SECONDS":   strconv.FormatFloat(duration.Seconds(), 'f', -1, 64),
		"RELEASE_PERF_PROCESS_PIDS":   string(encoded),
		"RELEASE_PERF_ACCEPTANCE_OUT": output,
	}, nil
}
