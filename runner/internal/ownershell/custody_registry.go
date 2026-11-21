package ownershell

import (
	"sort"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type CustodyState string

const (
	CustodyReserved  CustodyState = "reserved"
	CustodyActive    CustodyState = "active"
	CustodySuspended CustodyState = "suspended"
	CustodyClosing   CustodyState = "closing"
	CustodyClosed    CustodyState = "closed"
)

type CustodyClaim struct {
	Custody     protocol.ShellCustody `json:"custody"`
	State       CustodyState          `json:"state"`
	Transaction string                `json:"transaction,omitempty"`
	Allocated   bool                  `json:"allocated"`
}

// CustodyInventory is a bounded, complete snapshot for one physical scope.
// Authentication, current peer identity and one-use challenge ownership are the
// transport's responsibility; receipt of this struct alone confers no authority.
type CustodyInventory struct {
	Version   int            `json:"version"`
	Scope     string         `json:"scope"`
	Challenge string         `json:"challenge"`
	Revision  uint64         `json:"revision"`
	Complete  bool           `json:"complete"`
	Entries   []CustodyClaim `json:"entries"`
}

type CustodyReconciliation struct{ Eligible, Closing []CustodyClaim }

// CustodyRegistry is process-local authorization/cleanup state for #269. Closing
// records survive public handle removal. New process incarnations cannot recover
// it from a database, peer inventory, or copied row.
type CustodyRegistry struct {
	mu        sync.Mutex
	records   map[string]CustodyClaim
	revisions map[string]uint64
}

func NewCustodyRegistry() *CustodyRegistry {
	return &CustodyRegistry{records: map[string]CustodyClaim{}, revisions: map[string]uint64{}}
}

func (r *CustodyRegistry) countLocked(scope string) int {
	count := 0
	for _, c := range r.records {
		if c.Custody.PhysicalScope() == scope && c.State != CustodyClosed {
			count++
		}
	}
	return count
}

func (r *CustodyRegistry) pruneLocked(now time.Time) {
	for id, c := range r.records {
		if c.State == CustodyClosed && now.Unix() >= c.Custody.LifetimeUntil {
			delete(r.records, id)
		}
	}
	used := map[string]bool{}
	for _, claim := range r.records {
		used[claim.Custody.PhysicalScope()] = true
	}
	for scope := range r.revisions {
		if !used[scope] {
			delete(r.revisions, scope)
		}
	}
	// A future Reserve also refuses expired custody, so pruning an expired,
	// physically settled generation cannot permit its replay.
}

func (r *CustodyRegistry) Reserve(c protocol.ShellCustody) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Validate() != nil || time.Now().Unix() >= c.LifetimeUntil {
		return ErrDenied
	}
	r.pruneLocked(time.Now())
	id, scope := c.Digest(), c.PhysicalScope()
	if _, exists := r.records[id]; exists || len(r.records) >= 1024 || r.countLocked(scope) >= MaxShells {
		return ErrLimit
	}
	for _, existing := range r.records {
		if existing.Custody.PhysicalScope() == scope && existing.State == CustodyClosing {
			return Refuse(ReasonRuntimeFenced)
		}
	}
	r.records[id] = CustodyClaim{Custody: c, State: CustodyReserved}
	r.revisions[scope]++
	return nil
}

func (r *CustodyRegistry) Activate(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.records[id]
	if !ok || (c.State != CustodyReserved && c.State != CustodyActive && c.State != CustodySuspended) || time.Now().Unix() >= c.Custody.LifetimeUntil {
		return ErrDenied
	}
	c.State = CustodyActive
	c.Allocated = true
	r.records[id] = c
	r.revisions[c.Custody.PhysicalScope()]++
	return nil
}

func (r *CustodyRegistry) Suspend(scope string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.records {
		if c.Custody.PhysicalScope() == scope && c.State == CustodyActive {
			c.State = CustodySuspended
			r.records[id] = c
			r.revisions[scope]++
		}
	}
}

func (r *CustodyRegistry) BeginClose(id, transaction string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.records[id]
	if !ok || transaction == "" {
		return ErrDenied
	}
	if c.State == CustodyClosed {
		return nil
	}
	if c.Transaction != "" && c.Transaction != transaction {
		return ErrDenied
	}
	c.State = CustodyClosing
	c.Transaction = transaction
	r.records[id] = c
	r.revisions[c.Custody.PhysicalScope()]++
	return nil
}

func (r *CustodyRegistry) Count(scope string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.countLocked(scope)
}

func (r *CustodyRegistry) Snapshot(scope, challenge string) CustodyInventory {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := CustodyInventory{Version: 2, Scope: scope, Challenge: challenge, Revision: r.revisions[scope] + 1, Complete: true}
	for _, claim := range r.records {
		if claim.Custody.PhysicalScope() == scope && claim.State != CustodyClosed {
			snapshot.Entries = append(snapshot.Entries, claim)
		}
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		return snapshot.Entries[i].Custody.Digest() < snapshot.Entries[j].Custody.Digest()
	})
	return snapshot
}

func (r *CustodyRegistry) Reconcile(peer CustodyInventory, challenge string) (CustodyReconciliation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := CustodyReconciliation{}
	if peer.Version != 2 || peer.Scope == "" || challenge == "" || peer.Challenge != challenge || !peer.Complete || peer.Revision == 0 || len(peer.Entries) > MaxShells {
		return result, ErrUnknown
	}
	incoming := map[string]CustodyClaim{}
	for _, c := range peer.Entries {
		if c.Custody.Validate() != nil || c.Custody.PhysicalScope() != peer.Scope || (c.State != CustodyReserved && c.State != CustodyActive && c.State != CustodySuspended && c.State != CustodyClosing) {
			return result, ErrUnknown
		}
		id := c.Custody.Digest()
		if _, duplicate := incoming[id]; duplicate {
			return result, ErrUnknown
		}
		incoming[id] = c
	}
	// Validate the whole snapshot before mutating any local state.
	r.pruneLocked(time.Now())
	newRecords := 0
	for id := range incoming {
		if _, ok := r.records[id]; !ok {
			newRecords++
		}
	}
	if len(r.records)+newRecords > 1024 {
		return result, ErrLimit
	}
	for id, c := range incoming {
		if _, known := r.records[id]; !known {
			c.State = CustodyClosing
			c.Transaction = ""
			c.Allocated = true
			r.records[id] = c
		}
	}
	for id, local := range r.records {
		if local.Custody.PhysicalScope() != peer.Scope {
			continue
		}
		remote, found := incoming[id]
		if local.State == CustodyClosed && !found {
			continue
		}
		if !found || local.State == CustodyClosing || local.State == CustodyClosed || remote.State == CustodyClosing || local.State == CustodyReserved || remote.State == CustodyReserved {
			local.State = CustodyClosing
			r.records[id] = local
			result.Closing = append(result.Closing, local)
			continue
		}
		// Matching peer state is evidence for reconciliation, never a replacement
		// for the original local opening claim or authority/adoption checks.
		if local.State != CustodyReserved && remote.State != CustodyReserved {
			result.Eligible = append(result.Eligible, local)
		}
	}
	r.revisions[peer.Scope]++
	return result, nil
}

func (r *CustodyRegistry) DaemonRestart(scope, newIncarnation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.records {
		if c.Custody.PhysicalScope() == scope && c.Custody.Daemon != newIncarnation && c.State != CustodyClosed {
			c.State = CustodyClosing
			r.records[id] = c
		}
	}
	r.revisions[scope]++
}

func (r *CustodyRegistry) Settle(proof protocol.CustodyCleanup, responder string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.records[proof.Custody]
	if !ok || (c.State != CustodyClosing && c.State != CustodyClosed) || proof.ValidateFor(c.Custody, c.Transaction, responder) != nil {
		return ErrDenied
	}
	c.State = CustodyClosed
	r.records[proof.Custody] = c
	r.revisions[c.Custody.PhysicalScope()]++
	return nil
}

// AbsenceHold never consults persisted rows. The caller anchors absent to one
// absence generation and may not slide it on a hello or partial reconnect.
func (r *CustodyRegistry) AbsenceHold(session string, absent, now time.Time) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var until time.Time
	if absent.IsZero() {
		return until, false
	}
	capDeadline := absent.Add(protocol.DesktopShellLifetime)
	for _, c := range r.records {
		if c.Custody.Session != session || !c.Allocated || (c.State != CustodyActive && c.State != CustodySuspended) {
			continue
		}
		deadline := time.Unix(c.Custody.LifetimeUntil, 0)
		if capDeadline.Before(deadline) {
			deadline = capDeadline
		}
		if deadline.After(until) && now.Before(deadline) {
			until = deadline
		}
	}
	return until, !until.IsZero()
}

// ConsentCleanupState never upgrades missing ledger state into physical proof.
func (r *CustodyRegistry) ConsentCleanupState(owner, login, session, id string) (known, complete bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	complete = true
	for _, claim := range r.records {
		c := claim.Custody
		if c.Owner != owner || c.Login != login || c.Session != session || c.Consent != id {
			continue
		}
		known = true
		if claim.State != CustodyClosed {
			complete = false
		}
	}
	return known, known && complete
}

// cancelUnallocated drops only a local Reserved slot after the authenticated
// runtime confirms it never allocated this exact custody. No physical cleanup
// proof is fabricated and Active/Closing entries cannot take this path.
func (r *CustodyRegistry) cancelUnallocated(c protocol.ShellCustody) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	claim, ok := r.records[c.Digest()]
	if !ok || claim.Custody != c || claim.State != CustodyReserved || claim.Allocated {
		return false
	}
	delete(r.records, c.Digest())
	r.revisions[c.PhysicalScope()]++
	return true
}
