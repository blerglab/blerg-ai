package protocol

import "github.com/blerglab/blerg-ai/contracts/runtimelifecycle"

// Shared in-process lifecycle seam. Concrete implementations and transports are
// owned by P5; runtime coordinators P2/P3 may use fakes until integration.
type RuntimeSupervisor = runtimelifecycle.Supervisor
type SupervisorLaunch = runtimelifecycle.Launch
type SupervisorShell = runtimelifecycle.Shell
type SupervisorCleanup = runtimelifecycle.Cleanup
type SupervisorPhase = runtimelifecycle.Phase
