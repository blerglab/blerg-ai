package mcpgw

// Drift (docs/design/mcp-tool-changes.md §4): what the gateway does when an upstream's listing no
// longer matches a grant's pins. Everything here is idempotent per (kind, tool, hash) within one
// process, bounded by the change caps, and never fails the agent's request. Re-pins and grants
// happen only here, from tools/list, as compare-and-set writes carrying their pin event. A
// definition is stored only when its change row is recorded (or a policy re-pin or grant lands),
// so the caps bound the definition writes too.

import (
	"context"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// RepinsPerHour is how often auto_accept may re-pin one tool of one grant before the tool is
// treated as strict for the rest of the hour (a flapping server cannot bury its own audit).
const RepinsPerHour = 3

// refreshMinGap bounds how often one grant asks the daemon to have the agent re-list.
const refreshMinGap = time.Minute

const actorPolicy = "policy:auto_accept"

func driftKey(kind, tool, hash string) string { return kind + "\x00" + tool + "\x00" + hash }

// policyFor is the connection's pin policy, read from the cached credential. A built-in, or a
// credential that cannot be read, is strict: fail closed.
func (st *grantState) policyFor(ctx context.Context, gr *db.MCPGrant) string {
	if gr.Builtin != "" {
		return PinPolicyStrict
	}
	cred, err := st.credential(ctx, gr)
	if err != nil {
		return PinPolicyStrict
	}
	return NormalizePinPolicy(cred.PinPolicy)
}

// recordedDrift is one remembered RecordToolChange outcome. A Blocked outcome (declined or
// expired) is final for the session; anything else (inserted, already pending, capped) is re-asked
// after recordedTTL, because the person may have declined since, the row may have expired, or a
// cap may have freed.
type recordedDrift struct {
	out db.RecordOutcome
	at  time.Time
}

const recordedTTL = time.Minute

func (st *grantState) recordedOutcome(key string, now time.Time) (db.RecordOutcome, bool) {
	st.driftMu.Lock()
	defer st.driftMu.Unlock()
	r, ok := st.recorded[key]
	if !ok || (!r.out.Blocked && now.Sub(r.at) >= recordedTTL) {
		return db.RecordOutcome{}, false
	}
	return r.out, true
}

// remember keeps an outcome worth keeping: inserted, already pending, blocked or capped (a capped
// grant then costs one attempt per tool per recordedTTL, not one per list). A failed recording is
// not remembered, so the next request tries again.
func (st *grantState) remember(key, tool string, out db.RecordOutcome, now time.Time, drifting bool) {
	if !out.Inserted && !out.AlreadyPending && !out.Blocked && !out.Capped {
		return
	}
	st.driftMu.Lock()
	defer st.driftMu.Unlock()
	if st.recorded == nil {
		st.recorded = map[string]recordedDrift{}
	}
	st.recorded[key] = recordedDrift{out: out, at: now}
	if drifting && (out.Inserted || out.AlreadyPending) {
		if st.drifting == nil {
			st.drifting = map[string]bool{}
		}
		st.drifting[tool] = true
	}
}

// seedDrifting loads, once per state, which of the grant's tools have an open changed/removed
// row: a restart, a resume (a new token, so a new state) or another replica must still supersede
// a row when the pin matches again.
func (g *Gateway) seedDrifting(ctx context.Context, gr *db.MCPGrant, st *grantState) {
	st.driftMu.Lock()
	seeded := st.driftSeeded
	st.driftMu.Unlock()
	if seeded || gr.Builtin != "" {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	rows, err := db.ListOpenToolChanges(sctx, g.cfg.DB, gr.AccountID, gr.SessionID)
	if err != nil {
		log.Printf("mcpgw: seed drift state: %v", err)
		return // not seeded: tried again next request
	}
	st.driftMu.Lock()
	if st.drifting == nil {
		st.drifting = map[string]bool{}
	}
	for _, r := range rows {
		if r.ConnectionID == gr.ConnectionID && r.Kind != db.ToolChangeNew {
			st.drifting[r.Tool] = true
		}
	}
	st.driftSeeded = true
	st.driftMu.Unlock()
}

// supersede closes the tool's pending row (the pin matches again, or a policy re-pin or grant
// resolved it) and drops the tool's bookkeeping except the throttle. Unconditional: it does not
// depend on this process having recorded the row.
func (g *Gateway) supersede(ctx context.Context, gr *db.MCPGrant, st *grantState, tool string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := db.SupersedePendingToolChange(sctx, g.cfg.DB, gr.SessionID, gr.ConnectionID, tool); err != nil {
		log.Printf("mcpgw: supersede tool change: %v", err)
	}
	st.driftMu.Lock()
	delete(st.drifting, tool)
	for k := range st.recorded {
		if driftTool(k) == tool {
			delete(st.recorded, k)
		}
	}
	st.driftMu.Unlock()
}

// settled is called when a tool's live hash matches its pin again: a known open row is superseded.
func (g *Gateway) settled(ctx context.Context, gr *db.MCPGrant, st *grantState, tool string) {
	st.driftMu.Lock()
	was := st.drifting[tool]
	st.driftMu.Unlock()
	if was {
		g.supersede(ctx, gr, st, tool)
	}
}

// driftTool is the tool segment of a drift key.
func driftTool(key string) string {
	first, rest := -1, ""
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			first = i
			rest = key[i+1:]
			break
		}
	}
	if first < 0 {
		return ""
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == 0 {
			return rest[:i]
		}
	}
	return rest
}

// classify is ClassifyDefinition with a per-state cache by hash and no database write: the list
// path uses it for every served tool, so an unreviewable definition is never served, pinned or
// not (spec §3.2).
func (g *Gateway) classify(st *grantState, t upstreamTool) string {
	st.driftMu.Lock()
	u, ok := st.unsafeByHash[t.Hash]
	st.driftMu.Unlock()
	if ok {
		return u
	}
	canon, _ := CanonicalTool(t.Name, t.Description, t.InputSchema)
	u = ClassifyDefinition(canon)
	st.driftMu.Lock()
	if st.unsafeByHash == nil {
		st.unsafeByHash = map[string]string{}
	}
	st.unsafeByHash[t.Hash] = u
	st.driftMu.Unlock()
	return u
}

// reviewableChange reports whether the changed definition behind hash is known to be reviewable.
// With a fresh listing entry it is classified; without one (the cached hash answered inside the
// refresh gap) the class the fetch that opened the gap cached is read, and an unknown class fails
// closed.
func (g *Gateway) reviewableChange(st *grantState, live *upstreamTool, hash string) bool {
	if live != nil {
		return g.classify(st, *live) == ""
	}
	class, known := st.cachedClass(hash)
	return known && class == ""
}

// cachedClass is the remembered class of a definition hash (empty: reviewable), and whether this
// process has classified it.
func (st *grantState) cachedClass(hash string) (class string, known bool) {
	st.driftMu.Lock()
	defer st.driftMu.Unlock()
	class, known = st.unsafeByHash[hash]
	return class, known
}

// storeDefinition stores a definition (canonical bytes and class) the first time this process
// writes the hash. It is called only once a change row for the hash was recorded (or a policy
// re-pin or grant landed), so the change caps bound these writes too: a flapping or hostile
// upstream cannot grow the table past what the caps admit. Whether the definition is stored is
// tracked apart from the class cache: classify fills the class for a pinned-match tool without
// storing. A failed write is logged and tried again on the next call.
func (g *Gateway) storeDefinition(ctx context.Context, st *grantState, t upstreamTool) {
	st.driftMu.Lock()
	isStored := st.stored[t.Hash]
	st.driftMu.Unlock()
	if isStored {
		return
	}
	u := g.classify(st, t)
	canon, _ := CanonicalTool(t.Name, t.Description, t.InputSchema)
	d := db.ToolDefinition{Hash: t.Hash, Name: t.Name, Canonical: string(canon), Size: len(canon), Unsafe: u}
	if u == db.UnsafeTooLarge {
		d.Canonical = ""
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := db.UpsertToolDefinition(sctx, g.cfg.DB, d); err != nil {
		log.Printf("mcpgw: store tool definition: %v", err)
		return
	}
	st.driftMu.Lock()
	if st.stored == nil {
		st.stored = map[string]bool{}
	}
	st.stored[t.Hash] = true
	st.driftMu.Unlock()
}

// recordAndStore is recordChange plus, when the row exists (inserted or already pending), the
// definition the person will review. live is the fresh listing entry for newHash, nil when the
// call path answered from the cached hash (the definition is then stored by the list that
// recorded the change, or not at all).
func (g *Gateway) recordAndStore(ctx context.Context, gr *db.MCPGrant, st *grantState, kind, tool, oldHash, newHash, mode string, live *upstreamTool) db.RecordOutcome {
	out := g.recordChange(ctx, gr, st, kind, tool, oldHash, newHash, mode)
	if live != nil && (out.Inserted || out.AlreadyPending) {
		g.storeDefinition(ctx, st, *live)
	}
	return out
}

// recordChange records one drift row, once per (kind, tool, hash) per process. mode is what an
// accept would set (for `new`, the preset's mode). It returns the outcome so the call path can
// pick its refusal text; errors are logged and reported as a zero outcome.
func (g *Gateway) recordChange(ctx context.Context, gr *db.MCPGrant, st *grantState, kind, tool, oldHash, newHash, mode string) db.RecordOutcome {
	if gr.Builtin != "" {
		return db.RecordOutcome{}
	}
	now := g.now()
	key := driftKey(kind, tool, newHash)
	if out, ok := st.recordedOutcome(key, now); ok {
		return out
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	out, err := db.RecordToolChange(rctx, g.cfg.DB, db.ToolChange{
		AccountID: gr.AccountID, SessionID: gr.SessionID, ConnectionID: gr.ConnectionID, ConnectionName: gr.Name,
		Tool: tool, Kind: kind, OldHash: oldHash, NewHash: newHash, Mode: mode,
	}, g.cfg.ChangeLimits)
	if err != nil {
		log.Printf("mcpgw: record tool change (%s): %v", kind, err)
		return db.RecordOutcome{}
	}
	st.remember(key, tool, out, now, kind != db.ToolChangeNew)
	return out
}

// noteThrottled writes the throttled pin event once per tool per hour.
func (g *Gateway) noteThrottled(ctx context.Context, gr *db.MCPGrant, st *grantState, t upstreamTool, entry db.MCPGrantTool, now time.Time) {
	st.driftMu.Lock()
	last := st.throttleNoted[t.Name]
	if !last.IsZero() && now.Sub(last) < time.Hour {
		st.driftMu.Unlock()
		return
	}
	if st.throttleNoted == nil {
		st.throttleNoted = map[string]time.Time{}
	}
	st.throttleNoted[t.Name] = now
	st.driftMu.Unlock()
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := db.InsertPinEvent(ectx, g.cfg.DB, g.pinEvent(gr, t.Name, db.PinThrottled, entry.Hash, t.Hash, entry.Mode, actorPolicy)); err != nil {
		log.Printf("mcpgw: throttled event: %v", err)
	}
}

// throttled reports whether auto_accept has re-pinned tool RepinsPerHour times in the last hour.
func (st *grantState) throttled(tool string, now time.Time) bool {
	st.driftMu.Lock()
	defer st.driftMu.Unlock()
	kept := st.repins[tool][:0]
	for _, ts := range st.repins[tool] {
		if now.Sub(ts) < time.Hour {
			kept = append(kept, ts)
		}
	}
	if st.repins == nil {
		st.repins = map[string][]time.Time{}
	}
	st.repins[tool] = kept
	return len(kept) >= RepinsPerHour
}

func (st *grantState) noteRepin(tool string, now time.Time) {
	st.driftMu.Lock()
	if st.repins == nil {
		st.repins = map[string][]time.Time{}
	}
	st.repins[tool] = append(st.repins[tool], now)
	st.driftMu.Unlock()
}

// requestRefresh asks the daemon to have the agent re-list, at most once per refreshMinGap.
func (g *Gateway) requestRefresh(gr *db.MCPGrant, st *grantState) {
	r := g.refresherFn()
	if r == nil {
		return
	}
	now := g.now()
	st.driftMu.Lock()
	if !st.lastRefresh.IsZero() && now.Sub(st.lastRefresh) < refreshMinGap {
		st.driftMu.Unlock()
		return
	}
	st.lastRefresh = now
	st.driftMu.Unlock()
	r.RefreshSessionTools(gr.SessionID)
}

func (g *Gateway) pinEvent(gr *db.MCPGrant, tool, action, oldHash, newHash, mode, actor string) db.PinEvent {
	return db.PinEvent{AccountID: gr.AccountID, SessionID: gr.SessionID, ConnectionID: gr.ConnectionID, ConnectionName: gr.Name,
		Tool: tool, Action: action, OldHash: oldHash, NewHash: newHash, Mode: mode, Actor: actor}
}

// toolChangeHashBlocked is the database read behind hashBlocked, a variable so a test can fail it.
var toolChangeHashBlocked = db.ToolChangeHashBlocked

// hashBlocked reports whether the person declined (or let expire) a change to this hash in this
// session. A read error is logged and counts as blocked: the strict path then decides, which
// hides the tool for this request, so a transient error never lets auto_accept re-pin a hash
// the person declined.
func (g *Gateway) hashBlocked(ctx context.Context, gr *db.MCPGrant, t upstreamTool) bool {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	blocked, err := toolChangeHashBlocked(sctx, g.cfg.DB, gr.SessionID, gr.ConnectionID, t.Name, t.Hash)
	if err != nil {
		log.Printf("mcpgw: tool change blocked check: %v", err)
		return true
	}
	return blocked
}

// newToolMode is what a tool the launch never saw gets under the grant's preset ("" for custom).
func newToolMode(preset string) string {
	switch preset {
	case db.PresetAllowAll:
		return ModeAllow
	case db.PresetApproval:
		return ModePropose
	}
	return ""
}

// reconcileListing decides, for every upstream tool, whether tools/list serves it, applying the
// grant's pins, the connection's policy and the grant's preset (§4). It mutates gr.Tools in place
// for the entries it re-pinned or granted, returns the tools to serve and the {name: hash} pairs
// whose listed flag this response clears, and records whatever the person must review. A re-pin
// or grant asks for no refresh: this response already carries the new text to the process that
// listed (a call path that finds the tool changed does ask).
func (g *Gateway) reconcileListing(ctx context.Context, gr *db.MCPGrant, st *grantState, tools []upstreamTool, complete bool) (serve []upstreamTool, listed map[string]string) {
	listed = map[string]string{}
	if gr.Builtin != "" {
		// The session's board level (0.4.4), read now: a lowered level lists fewer tools.
		level, person := g.boardGrantAccess(ctx, gr)
		for _, t := range tools {
			entry, ok := gr.Tools[t.Name]
			if boardColumnToolSet[t.Name] {
				// Offered by the level and a person behind the access, not by a grant row.
				ok, entry = person != nil, db.MCPGrantTool{Mode: ModeAllow}
			}
			if ok && g.effectiveMode(entry, true) != "off" && toolPinOK(gr, entry, t.Name, t.Hash) && BoardLevelAllows(level, t.Name) {
				serve = append(serve, t)
			}
		}
		return serve, listed
	}
	count := map[string]int{}
	present := map[string]bool{}
	for _, t := range tools {
		count[t.Name]++
		present[t.Name] = true
	}
	known := map[string]bool{}
	for _, n := range gr.KnownTools {
		known[n] = true
	}
	g.seedDrifting(ctx, gr, st)
	policy := st.policyFor(ctx, gr)
	now := g.now()
	for _, t := range tools {
		if count[t.Name] > 1 {
			continue // a name listed twice is off, and never recorded
		}
		entry, granted := gr.Tools[t.Name]
		if granted {
			if g.effectiveMode(entry, true) == "off" {
				continue
			}
			if entry.Hash == t.Hash {
				g.settled(ctx, gr, st, t.Name)
				if g.classify(st, t) != "" {
					continue // pinned, but unreviewable: off everywhere (§3.2)
				}
				serve = append(serve, t)
				if !entry.IsListed() {
					listed[t.Name] = t.Hash
				}
				continue
			}
			unsafe := g.classify(st, t)
			// A declined or expired hash is never re-pinned: the person's decision outranks the
			// policy, and the code falls through to recordChange, which reports Blocked.
			if policy == PinPolicyAutoAccept && unsafe == "" && !g.hashBlocked(ctx, gr, t) {
				if st.throttled(t.Name, now) {
					g.noteThrottled(ctx, gr, st, t, entry, now)
					g.recordAndStore(ctx, gr, st, db.ToolChangeChanged, t.Name, entry.Hash, t.Hash, entry.Mode, &t)
					continue
				}
				wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				ok, err := db.RepinToolWithEvent(wctx, g.cfg.DB, gr.SessionID, gr.ConnectionID, t.Name, entry.Hash, t.Hash, "",
					g.pinEvent(gr, t.Name, db.PinRepinned, entry.Hash, t.Hash, entry.Mode, actorPolicy))
				cancel()
				if err != nil {
					log.Printf("mcpgw: auto-accept re-pin: %v", err)
				}
				if !ok {
					continue // lost the compare-and-set or failed: hidden this request
				}
				st.noteRepin(t.Name, now)
				g.storeDefinition(ctx, st, t)    // the audit and a later review of a reverted hash need the text
				g.supersede(ctx, gr, st, t.Name) // a strict row opened before the policy flipped (by any process) is superseded
				f := false
				entry.Hash, entry.Listed = t.Hash, &f
				gr.Tools[t.Name] = entry
				listed[t.Name] = t.Hash
				serve = append(serve, t)
				continue
			}
			g.recordAndStore(ctx, gr, st, db.ToolChangeChanged, t.Name, entry.Hash, t.Hash, entry.Mode, &t)
			continue
		}
		// Not granted: new only when the launch listing is known and did not contain it.
		if gr.KnownTools == nil || known[t.Name] {
			continue
		}
		unsafe := g.classify(st, t)
		mode := newToolMode(gr.Preset)
		if policy == PinPolicyAutoAccept && unsafe == "" && mode != "" {
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			ok, err := db.GrantToolWithEvent(wctx, g.cfg.DB, gr.SessionID, gr.ConnectionID, t.Name, t.Hash, mode,
				g.pinEvent(gr, t.Name, db.PinGranted, "", t.Hash, mode, actorPolicy))
			cancel()
			if err != nil {
				log.Printf("mcpgw: auto-accept grant: %v", err)
			}
			if !ok {
				continue
			}
			g.storeDefinition(ctx, st, t)
			g.supersede(ctx, gr, st, t.Name) // a `new` row opened before the policy flipped is superseded
			f := false
			gr.Tools[t.Name] = db.MCPGrantTool{Mode: mode, Hash: t.Hash, Listed: &f}
			if g.effectiveMode(gr.Tools[t.Name], true) != "off" {
				listed[t.Name] = t.Hash
				serve = append(serve, t)
			}
			continue
		}
		recordMode := mode
		if recordMode == "" {
			recordMode = ModePropose
		}
		g.recordAndStore(ctx, gr, st, db.ToolChangeNew, t.Name, "", t.Hash, recordMode, &t)
	}
	if complete {
		for name, entry := range gr.Tools {
			if !present[name] {
				g.recordChange(ctx, gr, st, db.ToolChangeRemoved, name, entry.Hash, "", entry.Mode)
			}
		}
	}
	return serve, listed
}

// markListed clears the listed flag of the entries this response served at their hash.
func (g *Gateway) markListed(ctx context.Context, gr *db.MCPGrant, listed map[string]string) {
	if len(listed) == 0 {
		return
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for name, hash := range listed {
		if _, err := db.MarkListed(mctx, g.cfg.DB, gr.SessionID, gr.ConnectionID, name, hash); err != nil {
			log.Printf("mcpgw: mark listed: %v", err)
		}
	}
}
