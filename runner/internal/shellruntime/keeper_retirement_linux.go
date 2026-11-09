package shellruntime

import (
	"context"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// Called only on the mutually authenticated daemon control stream. Closing a
// workspace is unconditional on its mutable digest. Detached custody remains
// inventoried and cannot be adopted or replaced until physical cleanup proves it.
func (k *KeeperRuntime) detachWorkspace(workspace string) {
	s := k.service
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.launches[workspace]
	if e == nil {
		return
	}
	e.mu.Lock()
	e.fenced, e.terminal = true, true
	e.phase = "stopping"
	e.mu.Unlock()
	e.retiring = true
	if s.hostRetiring == nil {
		s.hostRetiring = map[*registered]struct{}{}
	}
	s.hostRetiring[e] = struct{}{}
	delete(s.launches, workspace)
	if queue := s.queues[workspace]; queue != nil {
		close(queue.done)
		delete(s.queues, workspace)
	}
	if s.hostRetirementWake == nil {
		s.hostRetirementWake = make(chan struct{}, 1)
	}
	select {
	case s.hostRetirementWake <- struct{}{}:
	default:
	}
	if !s.hostRetirementRunning {
		s.hostRetirementRunning = true
		s.workers.Add(1)
		go k.retireDetached()
	}
}

func (k *KeeperRuntime) retireDetached() {
	s := k.service
	defer s.workers.Done()
	defer func() { s.mu.Lock(); s.hostRetirementRunning = false; s.mu.Unlock() }()
	backoff := 5 * time.Second
	select {
	case <-s.hostRetirementWake:
	default:
	}
	for {
		s.mu.Lock()
		entries := make([]*registered, 0, len(s.hostRetiring))
		for e := range s.hostRetiring {
			entries = append(entries, e)
		}
		s.mu.Unlock()
		for _, e := range entries {
			call, cancel := context.WithTimeout(s.hostLife, 2*time.Second)
			err := cleanupRetiredEntry(call, e)
			cancel()
			if err != nil {
				_ = bridgeFailure("retired_custody_cleanup", err)
				continue
			}
			s.mu.Lock()
			delete(s.hostRetiring, e)
			s.signalHostChangeLocked()
			s.mu.Unlock()
		}
		newTimer := s.retirementTimer
		if newTimer == nil {
			newTimer = func(d time.Duration) (<-chan time.Time, func()) {
				t := time.NewTimer(d)
				return t.C, func() { t.Stop() }
			}
		}
		tick, stop := newTimer(backoff)
		select {
		case <-s.hostLife.Done():
			stop()
			return
		case <-tick:
			backoff = nextRetirementBackoff(backoff)
		case <-s.hostRetirementWake:
			stop()
			backoff = 5 * time.Second
		}
	}
}

func cleanupRetiredEntry(ctx context.Context, e *registered) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	classes := append([]*retainedCoordinator(nil), e.retainedHistory...)
	if e.retained != nil {
		classes = append(classes, e.retained)
	}
	seenClasses := map[*retainedCoordinator]bool{}
	for _, c := range classes {
		if seenClasses[c] {
			continue
		}
		seenClasses[c] = true
		if err := c.CloseAll(ctx, "workspace_retired"); err != nil {
			return err
		}
	}
	ordinary := append([]*Coordinator(nil), e.containmentHistory...)
	if e.coordinator != nil {
		ordinary = append(ordinary, e.coordinator)
	}
	seen := map[*Coordinator]bool{}
	for _, c := range ordinary {
		if seen[c] {
			continue
		}
		seen[c] = true
		launch := ownershell.SupervisorIdentity(c.binding, ownershell.SignedGrant{}).Launch
		if err := c.Notify(ctx, launch, "replacing"); err != nil {
			return err
		}
		proof, err := c.CloseLaunch(ctx, launch, "workspace_retired")
		if err != nil {
			return err
		}
		if err = proof.ValidateFor(launch, ""); err != nil {
			return err
		}
	}
	return joinShellOutput(ctx, e)
}
