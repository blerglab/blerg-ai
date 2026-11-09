//go:build linux

package shellruntime

import (
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PrepareKeeperRuntime runs under keeper.lock before binding the listener.
// The installed keeper unit owns its entire cgroup; residual tasks from a dead
// tracer are never equivalent to an empty in-memory custody index.
func PrepareKeeperRuntime() error {
	return prepareKeeperRuntime(ownCgroup, SweepShellCgroups)
}

func prepareKeeperRuntime(own func() (string, error), sweep func() error) error {
	root, err := own()
	if err != nil {
		_ = bridgeFailure("keeper_containment_unavailable", err)
		return shellkeeper.ErrUnsupported
	}
	if err := sweep(); err != nil {
		return bridgeFailure("orphan_cgroups", err)
	}
	if filepath.Base(root) != "blerg-shell-keeper.service" {
		return nil
	} // direct development launches share a session scope
	data, err := os.ReadFile(filepath.Join(root, "cgroup.procs")) //nolint:gosec // kernel-derived cgroup mount and membership, fixed kernel file
	if err != nil {
		return bridgeFailure("keeper_tasks", err)
	}
	return keeperGroupEmptyExceptSelf(string(data), os.Getpid())
}
func keeperGroupEmptyExceptSelf(data string, self int) error {
	found := false
	for _, field := range strings.Fields(data) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 || pid != self {
			return bridgeFailure("keeper_residual_tasks", ErrClosed)
		}
		found = true
	}
	if !found {
		return bridgeFailure("keeper_membership", ErrClosed)
	}
	return nil
}
