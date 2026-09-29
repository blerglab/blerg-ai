package daemon

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Model probes: each engine that can say which models this workstation may
// use declares an EngineSpec.ListModels hook (codex: `codex debug models`;
// hermes: its configured endpoint's /models). The ModelProber runs every
// hook at start, on (re)connect and then every ~15 minutes, off the
// heartbeat and spawn paths, and keeps the last result per engine for the
// WSClient to report (DaemonHello/DaemonHeartbeat.EngineModels). A new
// engine gets a list by adding a hook; nothing here names an engine.

const (
	// modelProbeInterval is how often the lists are re-probed, give or take
	// modelProbeJitter (so a fleet of daemons does not probe in lockstep).
	modelProbeInterval = 15 * time.Minute
	modelProbeJitter   = 2 * time.Minute
	// modelProbeMinGap: a connect-triggered probe this soon after the last
	// one is skipped, so a flapping connection cannot spawn a probe per
	// reconnect attempt.
	modelProbeMinGap = time.Minute
	// modelProbeDeadline is the outer bound on one probe; each hook bounds
	// itself tighter (codex 10 s, hermes 3 s).
	modelProbeDeadline = 15 * time.Second
	// probeAbandonGrace is how long past its deadline ProbeAll still waits
	// for a probe before abandoning it.
	probeAbandonGrace = 2 * time.Second
)

// errNoModelList is what a ListModels hook returns for a definite "this
// engine has nothing to offer here" — not installed, not configured for a
// listable provider, or the endpoint refused. The engine is then reported
// with an empty list. Any other error is treated as transient: the last good
// list is kept.
var errNoModelList = errors.New("no model list")

// modelProbe is one engine's ListModels hook.
type modelProbe func(ctx context.Context) ([]models.Model, error)

// ModelProber runs the engines' model probes and holds their latest results.
// Safe for concurrent use.
type ModelProber struct {
	probes   map[string]modelProbe
	interval time.Duration
	jitter   time.Duration
	minGap   time.Duration
	deadline time.Duration
	grace    time.Duration // see probeAbandonGrace
	now      func() time.Time

	kick    chan struct{}
	changed chan struct{}

	mu       sync.Mutex
	reports  map[string]models.Report
	gen      uint64 // bumped whenever a list changes
	lastRun  time.Time
	inflight map[string]bool // engines whose probe goroutine has not returned yet
}

// NewModelProber returns a prober for every registered engine whose
// EngineSpec has a ListModels hook.
func NewModelProber() *ModelProber {
	probes := map[string]modelProbe{}
	for id, spec := range engineRegistry {
		if spec.ListModels != nil {
			probes[id] = spec.ListModels
		}
	}
	return newModelProber(probes)
}

func newModelProber(probes map[string]modelProbe) *ModelProber {
	return &ModelProber{
		probes:   probes,
		interval: modelProbeInterval,
		jitter:   modelProbeJitter,
		minGap:   modelProbeMinGap,
		deadline: modelProbeDeadline,
		grace:    probeAbandonGrace,
		now:      time.Now,
		kick:     make(chan struct{}, 1),
		changed:  make(chan struct{}, 1),
		reports:  map[string]models.Report{},
		inflight: map[string]bool{},
	}
}

// Snapshot returns a copy of every engine's latest report (nil before any
// probe has finished) and the generation it belongs to: the generation
// changes exactly when some list does, so a sender can report on change only.
func (p *ModelProber) Snapshot() (map[string]models.Report, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reports) == 0 {
		return nil, p.gen
	}
	out := make(map[string]models.Report, len(p.reports))
	for e, r := range p.reports {
		ms := make([]models.Model, len(r.Models))
		for i, m := range r.Models {
			m.Efforts = append([]string{}, m.Efforts...)
			ms[i] = m
		}
		out[e] = models.Report{Models: ms, FetchedAt: r.FetchedAt}
	}
	return out, p.gen
}

// Changed is signalled (coalesced) whenever the generation moves.
func (p *ModelProber) Changed() <-chan struct{} { return p.changed }

// Kick asks Run for a probe now (a connect). Never blocks; a kick within
// minGap of the last probe is ignored by Run.
func (p *ModelProber) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run probes now, then every interval (± jitter) and on Kick, until ctx ends.
func (p *ModelProber) Run(ctx context.Context) {
	if len(p.probes) == 0 {
		return
	}
	p.ProbeAll(ctx)
	for {
		wait := p.interval
		if p.jitter > 0 {
			wait += time.Duration(rand.Int64N(int64(2*p.jitter))) - p.jitter //nolint:gosec // probe-interval jitter, not a security value
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			p.ProbeAll(ctx)
		case <-p.kick:
			t.Stop()
			p.mu.Lock()
			recent := !p.lastRun.IsZero() && p.now().Sub(p.lastRun) < p.minGap
			p.mu.Unlock()
			if !recent {
				p.ProbeAll(ctx)
			}
		}
	}
}

// errProbeAbandoned: the probe did not return within the prober's deadline
// (it ignored its context), or an earlier run of it still has not returned.
var errProbeAbandoned = errors.New("probe did not return in time")

// ProbeAll runs every probe concurrently, each under the prober's deadline,
// and records the results. It waits for no probe longer than the deadline
// plus a short grace: a probe that ignores its context is abandoned (counted
// as a transient failure, so its last good list stays) rather than stalling
// the others, and is not started again until it has returned.
func (p *ModelProber) ProbeAll(ctx context.Context) {
	type result struct {
		engine string
		models []models.Model
		err    error
	}
	results := make(chan result, len(p.probes)) // abandoned probes still land here, never block
	pending := map[string]bool{}
	var all []result
	for engine, probe := range p.probes {
		p.mu.Lock()
		busy := p.inflight[engine]
		if !busy {
			p.inflight[engine] = true
		}
		p.mu.Unlock()
		if busy {
			all = append(all, result{engine, nil, errProbeAbandoned})
			continue
		}
		pending[engine] = true
		go func() {
			defer func() {
				p.mu.Lock()
				delete(p.inflight, engine)
				p.mu.Unlock()
			}()
			pctx, cancel := context.WithTimeout(ctx, p.deadline)
			defer cancel()
			ms, err := probe(pctx)
			results <- result{engine, ms, err}
		}()
	}
	hard := time.NewTimer(p.deadline + p.grace)
	defer hard.Stop()
collect:
	for len(pending) > 0 {
		select {
		case r := <-results:
			delete(pending, r.engine)
			all = append(all, r)
		case <-hard.C:
			break collect
		}
	}
	for engine := range pending {
		all = append(all, result{engine, nil, errProbeAbandoned})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].engine < all[j].engine })

	p.mu.Lock()
	now := p.now().UTC()
	p.lastRun = now
	changed := false
	for _, r := range all {
		prev, had := p.reports[r.engine]
		var next []models.Model
		switch {
		case r.err == nil:
			next = models.SanitizeReported(r.engine, r.models)
		case errors.Is(r.err, errNoModelList):
			next = []models.Model{}
		default:
			log.Printf("models: %s model probe failed (keeping the last list): %v", r.engine, r.err)
			if had {
				continue
			}
			next = []models.Model{}
		}
		if !had || !reflect.DeepEqual(prev.Models, next) {
			changed = true
		}
		p.reports[r.engine] = models.Report{Models: next, FetchedAt: now}
	}
	if changed {
		p.gen++
	}
	p.mu.Unlock()
	if changed {
		select {
		case p.changed <- struct{}{}:
		default:
		}
	}
}
