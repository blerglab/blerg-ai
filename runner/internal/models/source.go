package models

import (
	"context"
	"sync"
	"time"
)

// Where a List came from.
const (
	SourceLive    = "live"    // fetched from the engine's own published list
	SourceBuiltin = "builtin" // the list compiled into this build
	SourceNone    = "none"    // no source for the engine, or nothing reported for it
	SourceDaemon  = "daemon"  // probed and reported by a connected daemon
)

// Model is one picker entry, engine-neutral. By the time a Source hands it
// out it is validated: ID passes the engine's model rule, every effort is in
// the engine's allowlist (lowest first), and DefaultEffort is one of Efforts
// or "" when there are none.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Section       string   `json:"section"` // "main" | "overflow"
	Efforts       []string `json:"efforts"` // never nil: [] = takes no effort
	DefaultEffort string   `json:"default_effort,omitempty"`
	// EffortKind optionally names what the effort control means for this
	// model (e.g. "reasoning"), for engines whose UI words it differently.
	EffortKind string `json:"effort_kind,omitempty"`
}

// List is what GET /api/models/{engine} serves.
type List struct {
	Models    []Model    `json:"models"`
	Source    string     `json:"source"` // SourceLive | SourceBuiltin | SourceDaemon | SourceNone
	FetchedAt *time.Time `json:"fetched_at"`
	// DaemonID names the daemon a SourceDaemon list came from. Empty for every
	// other source, and for a SourceDaemon union of several daemons' lists.
	DaemonID string `json:"daemon_id,omitempty"`
	// EngineEfforts is the engine's whole effort allowlist, lowest first
	// ([] when it takes none) — what a model typed by hand may be run at.
	// Filled by the server's handler from the rules table, not by a Source.
	EngineEfforts []string `json:"engine_efforts"`
}

// Source supplies one engine's model list. daemonID is the daemon the caller
// is about to launch on ("" when unknown or not applicable) — for engines
// whose list is reported per daemon (a CLI's own `models` command) rather
// than published centrally.
type Source interface {
	Models(ctx context.Context, daemonID string) (List, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context, daemonID string) (List, error)

// Models calls f.
func (f SourceFunc) Models(ctx context.Context, daemonID string) (List, error) {
	return f(ctx, daemonID)
}

// Registry maps engine ids to their Source. Safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{sources: map[string]Source{}} }

// Register sets engine's source ("" means Claude, as everywhere else).
func (r *Registry) Register(engine string, s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[NormalizeEngine(engine)] = s
}

// Models returns engine's list, or an empty SourceNone list for an engine
// nothing is registered for — "no picker", not an error.
func (r *Registry) Models(ctx context.Context, engine, daemonID string) (List, error) {
	r.mu.RLock()
	s, ok := r.sources[NormalizeEngine(engine)]
	r.mu.RUnlock()
	if !ok {
		return List{Models: []Model{}, Source: SourceNone}, nil
	}
	return s.Models(ctx, daemonID)
}
