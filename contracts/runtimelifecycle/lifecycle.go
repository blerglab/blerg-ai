// Package runtimelifecycle defines the seam between runtime settlement and shell
// supervisors. It contains no transport, process containment or policy service.
package runtimelifecycle

import (
	"context"
	"errors"
)

const (
	MaxShells            = 4
	MaxFrameBytes        = 16 << 10
	OutputQueueBytes     = 256 << 10 // per handle; rates below are shared per session
	OutputBytesPerSecond = 64 << 10
	InputBytesPerSecond  = 16 << 10
)

// Launch binds supervisor operations to trusted runtime connection state. Runtime
// generation is allocated by the server independently of selection generation.
// None of these identities may be obtained from agent files or request authority.
type Launch struct {
	SessionID         string `json:"session_id"`
	LaunchGeneration  string `json:"launch_generation"`
	RuntimeGeneration int64  `json:"runtime_generation"`
	Runtime           string `json:"runtime"`
	ConnectionID      string `json:"connection_id"`
}

func (b Launch) Validate() error {
	if b.SessionID == "" || b.LaunchGeneration == "" || b.RuntimeGeneration <= 0 || b.ConnectionID == "" || (b.Runtime != "host" && b.Runtime != "sandbox" && b.Runtime != "cluster") {
		return errors.New("invalid supervisor launch")
	}
	return nil
}

type Shell struct {
	Launch
	ShellGeneration string `json:"shell_generation"`
}

func (s Shell) Validate() error {
	if err := s.Launch.Validate(); err != nil {
		return err
	}
	if s.ShellGeneration == "" {
		return errors.New("invalid supervisor shell")
	}
	return nil
}

type Phase string

const (
	Running   Phase = "running"
	Syncing   Phase = "syncing"
	Paused    Phase = "paused"
	Stopping  Phase = "stopping"
	Replacing Phase = "replacing"
	Ended     Phase = "ended"
	Discarded Phase = "discarded"
)

func (p Phase) Valid() bool {
	switch p {
	case Running, Syncing, Paused, Stopping, Replacing, Ended, Discarded:
		return true
	}
	return false
}

// Cleanup is a no-writers proof, not a transport-close receipt. Empty
// ShellGeneration means ALL supervisors of this launch, including detached
// descendants and orphans, not just the four currently visible shell handles.
// Storage release requires an all-launch proof plus P2/P3's agent/runtime cleanup.
type Cleanup struct {
	Launch
	ShellGeneration  string `json:"shell_generation,omitempty"`
	ContainmentEmpty bool   `json:"containment_empty"`
	PTYsClosed       bool   `json:"ptys_closed"`
	Reaped           bool   `json:"reaped"`
}

func (p Cleanup) ValidateFor(b Launch, shellGeneration string) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if p.Launch != b || p.ShellGeneration != shellGeneration || !p.ContainmentEmpty || !p.PTYsClosed || !p.Reaped {
		return errors.New("supervisor cleanup unproven")
	}
	return nil
}

// Supervisor is implemented by P5 and faked by P2/P3. All operations are bound
// to authenticated current runtime state. Policy/grant checks remain mandatory;
// this lifecycle interface confers no shell authority.
//
// The runtime lifecycle coordinator owns ONE session mutex. AdmitShell, Notify,
// CloseLaunch and RecoverLaunch must serialize their state transitions under it.
// AdmitShell atomically checks the admission fence and reserves a slot before
// starting writers; at most four slots share one session resource/rate budget.
// Notify synchronously fences admission before acknowledging any non-running
// phase. Syncing updates the header before P2/P3 freeze; no method may exec into
// a paused container. Running alone cannot clear an uncertain/terminal fence.
//
// CloseShell/CloseLaunch are idempotent, fence first, then kill all contained
// descendants, close PTYs and reap independently of output delivery. CloseLaunch
// covers ALL shell supervisors, including pending admissions and detached jobs.
// Cancellation, timeout, unreachable peers or errors never constitute cleanup;
// they keep admission fenced and storage retained. Only an exact ValidateFor
// proof may acknowledge completion; old connections/generations cannot unfence.
//
// RecoverLaunch reconciles current runtime identity and cleans orphan supervisors
// BEFORE returning an all-launch proof. Initially/restarted runtimes are fenced.
// Only verified recovery of a live running launch may allow later admissions;
// end/discard/replacement and expired/revoked grants never resurrect shells.
// Implementations must bound calls by ctx, with independent cleanup continuing
// when request/output delivery is canceled. They must not record shell payloads.
type Supervisor interface {
	AdmitShell(context.Context, Shell) error
	Notify(context.Context, Launch, Phase) error
	CloseShell(context.Context, Shell, string) (Cleanup, error)
	CloseLaunch(context.Context, Launch, string) (Cleanup, error)
	RecoverLaunch(context.Context, Launch) (Cleanup, error)
}
