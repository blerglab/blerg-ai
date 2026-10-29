package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
)

type liveChild struct {
	Runtime        string     `json:"runtime,omitempty"`
	UsageStatus    string     `json:"usage_status"`
	ToolCalls      int64      `json:"tool_calls"`
	Activity       string     `json:"activity,omitempty"`
	ActivitySince  *time.Time `json:"activity_since,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	Local          bool       `json:"local,omitempty"`
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Engine         string     `json:"engine"`
	Model          string     `json:"model"`
	Effort         string     `json:"effort"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at"`
	Asking         bool       `json:"asking"`
	Tokens         *int64     `json:"tokens"`
	CostUSD        *float64   `json:"cost_usd,omitempty"`
	// Usage is Tokens broken down for the Running now chip's tooltip (live_usage.go); present
	// exactly when Tokens is.
	Usage *liveUsage `json:"usage,omitempty"`
	// Ticket is the started session's current pin (the newest it pinned and still holds): the
	// parent's panel shows it on the child's row.
	Ticket *liveChildTicket `json:"ticket,omitempty"`
}

// liveChildTicket is a child's current pin, the summary fields only.
type liveChildTicket struct {
	Source  string `json:"source"`
	Key     string `json:"key"`
	BoardID string `json:"board_id,omitempty"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
}

type liveDelegationRequest struct {
	RequestID string    `json:"request_id"`
	Engine    string    `json:"engine"`
	Title     string    `json:"title"`
	ExpiresAt time.Time `json:"expires_at"`
}

type liveDelegationGrant struct {
	ID         string `json:"id"`
	DaemonID   string `json:"daemon_id"`
	DaemonName string `json:"daemon_name"`
	Engine     string `json:"engine"`
	Runtime    string `json:"runtime"`
	Repo       string `json:"repo,omitempty"`
	NoRepo     bool   `json:"no_repo,omitempty"`
}

type liveDelegation struct {
	Children           []liveChild             `json:"children"`
	DelegationRequests []liveDelegationRequest `json:"delegation_requests"`
	DelegationGrants   []liveDelegationGrant   `json:"delegation_grants"`
}

type childrenSnapshot struct {
	Rows     []db.ChildLiveRow
	Requests []liveDelegationRequest
	Grants   []liveDelegationGrant
}

func (a *API) readChildrenSnapshot(ctx context.Context, parent *db.SessionRow, account string) (childrenSnapshot, error) {
	rows, err := db.ListVisibleChildLiveRows(ctx, a.dbPool, parent.ID, account)
	if err != nil {
		return childrenSnapshot{}, err
	}
	visible := rows[:0]
	for _, row := range rows {
		if row.ArchivedAt == nil {
			row.Local = a.hub.FindDaemonForSession(row.ID).LocalEngine(row.Engine)
			visible = append(visible, row)
		}
	}
	out := childrenSnapshot{Rows: visible, Requests: []liveDelegationRequest{}, Grants: []liveDelegationGrant{}}
	requests, err := db.ListPendingDelegationRequests(ctx, a.dbPool, parent.ID)
	if err != nil {
		return childrenSnapshot{}, err
	}
	for _, request := range requests {
		if !request.ExpiresAt.After(time.Now()) {
			continue
		}
		var stored storedChildRequest
		if err := json.Unmarshal(request.Request, &stored); err != nil {
			return childrenSnapshot{}, err
		}
		out.Requests = append(out.Requests, liveDelegationRequest{RequestID: request.ID, Engine: stored.Engine, Title: stored.Title, ExpiresAt: request.ExpiresAt})
	}
	if ownsSession(account, parent) {
		grants, err := db.ListLiveDelegationGrants(ctx, a.dbPool, parent.ID)
		if err != nil {
			return childrenSnapshot{}, err
		}
		daemonNames := make(map[string]string)
		for _, g := range grants {
			if g.DaemonID != clusterDaemonID && !a.hub.ownsDesktopID(ctx, account, g.DaemonID) {
				continue
			}
			name, known := daemonNames[g.DaemonID]
			if !known && g.DaemonID == clusterDaemonID {
				name, known = clusterDaemonLabel, true
			}
			if !known {
				var err error
				name, _, err = db.HostDaemonName(ctx, a.dbPool, g.DaemonID)
				if err != nil {
					return childrenSnapshot{}, err
				}
				daemonNames[g.DaemonID] = name
			}
			out.Grants = append(out.Grants, liveDelegationGrant{ID: g.ID, DaemonID: g.DaemonID, DaemonName: name, Engine: g.Engine, Runtime: g.Runtime, Repo: g.Repo, NoRepo: g.NoRepo})
		}
	}
	return out, nil
}

func (s childrenSnapshot) digest() string {
	// Include every child's local sequence, not only max(seq) across children:
	// activity in a quieter child must invalidate a louder sibling's cached view.
	raw, _ := json.Marshal(s)
	return childrenDigestBytes(raw)
}

func childrenDigestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:16])
}

func (a *API) liveChildren(ctx context.Context, rows []db.ChildLiveRow, account string, showTokens, admin bool, prices []db.ModelPrice) ([]liveChild, []db.SessionTokenRow, error) {
	out := make([]liveChild, 0, len(rows))
	var allUsage []db.SessionTokenRow
	for _, row := range rows {
		c := liveChild{Runtime: row.Runtime, ID: row.ID, Title: row.Title, Engine: row.Engine, Model: row.Model, Effort: row.Effort, Status: row.Status,
			StartedAt: row.StartedAt, EndedAt: row.EndedAt, Asking: row.OpenAsks > 0}
		snapshot, err := db.ReadChildTelemetry(ctx, a.dbPool, row.ID, row.MaxSeq)
		if err != nil {
			return nil, nil, err
		}
		progress := childTelemetry(&c, snapshot)
		pins, err := db.ListTicketRows(ctx, a.dbPool, row.ID, row.MaxSeq)
		if err != nil {
			return nil, nil, err
		}
		if cur := ticket.Pinned(pins); len(cur) > 0 {
			p := cur[0]
			c.Ticket = &liveChildTicket{Source: p.Source, Key: p.Key, BoardID: p.BoardID, Title: p.Title, Status: p.Status, URL: p.URL}
		}
		c.UsageStatus = "hidden"
		if showTokens && (admin || account != "" && account == row.Owner) {
			usage, err := db.ListSessionTokenUsageThrough(ctx, a.dbPool, row.ID, row.MaxSeq)
			if err != nil {
				return nil, nil, err
			}
			c.UsageStatus = "pending"
			displayUsage := append([]db.SessionTokenRow(nil), usage...)
			if len(usage) > 0 {
				c.UsageStatus = "reported"
			}
			if progress != nil {
				c.UsageStatus = "pending"
				if progress.Usage != nil {
					u := progress.Usage
					c.UsageStatus = "reported"
					displayUsage = append(displayUsage, db.SessionTokenRow{Model: progress.Model, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
				}
			}
			summary := summariseSessionTokens(displayUsage, prices)
			tokens := summary.Totals.Input + summary.Totals.Output + summary.Totals.CacheRead + summary.Totals.CacheWrite
			if len(displayUsage) > 0 && (c.UsageStatus != "pending" || tokens != 0) {
				c.Tokens = &tokens
			}
			if len(summary.UnpricedModels) == 0 && c.Tokens != nil {
				c.CostUSD = summary.Totals.CostUSD
			}
			if c.Tokens != nil {
				turn, inFlight := turnUsage(snapshot.Events, c.EndedAt == nil && (c.Status == "running" || c.Status == "waiting"))
				c.Usage = summariseLiveUsage(usage, inFlight, turn, prices)
				if c.Usage != nil {
					if c.Usage.Context, err = a.readLiveContext(ctx, row.ID, row.MaxSeq); err != nil {
						return nil, nil, err
					}
				}
			}
			c.Local = row.Local
			allUsage = append(allUsage, usage...)
		}
		out = append(out, c)
	}
	return out, allUsage, nil
}

type startedSessions struct {
	Count    int         `json:"count"`
	Sessions []liveChild `json:"sessions"`
}

const maxChildParentCacheEntries = 4096

type childrenNotifier struct {
	cacheOrder []string
	cacheNext  int
	mu         sync.Mutex
	parentOf   map[string]string
	last       map[string]time.Time
	origin     map[string]string
	pending    map[string]bool
	throttle   time.Duration
}

// rememberParent bounds positive and negative lookups with a fixed FIFO ring.
// The caller holds mu; eviction only causes a fresh authoritative lookup.
func (n *childrenNotifier) rememberParent(id, parent string) {
	if n.parentOf == nil {
		n.parentOf = make(map[string]string)
	}
	if _, exists := n.parentOf[id]; !exists {
		if len(n.cacheOrder) < maxChildParentCacheEntries {
			n.cacheOrder = append(n.cacheOrder, id)
		} else {
			delete(n.parentOf, n.cacheOrder[n.cacheNext])
			n.cacheOrder[n.cacheNext] = id
			n.cacheNext = (n.cacheNext + 1) % maxChildParentCacheEntries
		}
	}
	n.parentOf[id] = parent
}

// reserveThrottleParent keeps every retained parent map (and therefore the
// number of trailing timers) bounded. Pending timers are never evicted. When
// all slots are busy, the caller sends a scoped hint without retaining state.
// The caller holds mu.
func (n *childrenNotifier) reserveThrottleParent(parent string, now time.Time, interval time.Duration) bool {
	if _, exists := n.last[parent]; exists || len(n.last) < maxChildParentCacheEntries {
		return true
	}
	oldest := ""
	var oldestAt time.Time
	for id, at := range n.last {
		if n.pending[id] || at.Add(interval).After(now) {
			continue
		}
		if oldest == "" || at.Before(oldestAt) {
			oldest, oldestAt = id, at
		}
	}
	if oldest == "" {
		return false
	}
	delete(n.last, oldest)
	delete(n.origin, oldest)
	delete(n.pending, oldest)
	return true
}

func (h *Hub) SetChildActivityHook(fn func(string)) {
	h.mu.Lock()
	h.childActivity = fn
	h.mu.Unlock()
}

func (h *Hub) ChildActivity(id string) {
	h.mu.RLock()
	fn := h.childActivity
	h.mu.RUnlock()
	if fn != nil {
		fn(id)
	}
}

func runningChildren(children []liveChild) int {
	n := 0
	for _, c := range children {
		if c.EndedAt == nil && !terminalSessionStatus(c.Status) {
			n++
		}
	}
	return n
}

func (a *API) noteChildActivity(id string) {
	if a.dbPool == nil {
		return
	}
	n := &a.children
	n.mu.Lock()
	parent, cached := n.parentOf[id]
	n.mu.Unlock()
	if !cached {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var err error
		parent, err = db.ParentOf(ctx, a.dbPool, id)
		if err != nil {
			return
		}
		n.mu.Lock()
		n.rememberParent(id, parent)
		n.mu.Unlock()
	}
	if parent == "" {
		return
	}
	n.mu.Lock()
	if n.last == nil {
		n.last = make(map[string]time.Time)
		n.pending = make(map[string]bool)
		n.origin = make(map[string]string)
	}
	interval := n.throttle
	if interval <= 0 {
		interval = childrenChangedThrottle
	}
	now := time.Now()
	if !n.reserveThrottleParent(parent, now, interval) {
		n.mu.Unlock()
		a.broadcastChildrenChanged(parent, id)
		return
	}
	n.origin[parent] = id
	if n.pending[parent] {
		n.mu.Unlock()
		return
	}
	remaining := n.last[parent].Add(interval).Sub(now)
	if remaining <= 0 {
		n.last[parent] = now
		n.mu.Unlock()
		a.broadcastChildrenChanged(parent, id)
		return
	}
	n.pending[parent] = true
	n.mu.Unlock()
	time.AfterFunc(remaining, func() {
		n.mu.Lock()
		origin := n.origin[parent]
		delete(n.pending, parent)
		n.last[parent] = time.Now()
		n.mu.Unlock()
		a.broadcastChildrenChanged(parent, origin)
	})
}

func (a *API) broadcastChildrenChanged(parent, source string) {
	a.hub.BroadcastJSON(map[string]string{"type": "children_changed", "session_id": parent, "source_session_id": source})
}
