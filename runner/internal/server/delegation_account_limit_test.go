package server

import "testing"

// The admin's "workers at once, per agent" setting must never be undercut by the
// hidden per-account cap: on 2026-11-05 the owner set 8 and every start past 6 was refused.
func TestAccountRunningLimitHonorsTheConfiguredWorkerLimit(t *testing.T) {
	for _, tc := range []struct{ parallel, want int }{
		{3, maxRunningChildrenPerAccount},
		{maxRunningChildrenPerAccount, maxRunningChildrenPerAccount},
		{8, 8},
		{50, 50},
	} {
		if got := accountRunningLimit(childStartLimits{parallel: tc.parallel, hourly: 120}); got != tc.want {
			t.Errorf("parallel %d: account limit %d, want %d", tc.parallel, got, tc.want)
		}
	}
}
