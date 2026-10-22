package server

// The limits on a session's board calls (0.4.5). An administrator sets them in core's Settings
// (core identity.BoardLimits); the runner reads them from core (POST /internal/board-limits, its
// internal key) and keeps them for boardLimitsTTL, so a change takes effect at a session's next board
// call once the runner has read it, within a minute. Until the first read, and whenever core cannot
// be asked (no core configured, an older core, an error), the runner's own defaults apply:
// boardCallsPerMinute, boardCallsInFlight and the gateway's unrevoked allowance. A failed read is
// retried after boardLimitsRetry, keeping the last values it read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	boardLimitsTTL   = time.Minute
	boardLimitsRetry = 15 * time.Second
)

// coreBoardLimits is the runner's part of core's board call limits (the live-token cap is core's).
type coreBoardLimits struct {
	CallsPerMinute      int `json:"calls_per_minute"`
	CallsInFlight       int `json:"calls_in_flight"`
	UnrevokedPerAccount int `json:"unrevoked_per_account"`
}

// valid: the ranges core's settings allow (core identity.BoardLimitRange and migration 023's
// CHECKs; keep the three in step).
func (l coreBoardLimits) valid() bool {
	return l.CallsPerMinute >= 1 && l.CallsPerMinute <= 10000 && l.CallsInFlight >= 1 && l.CallsInFlight <= 64 &&
		l.UnrevokedPerAccount >= 1 && l.UnrevokedPerAccount <= 1000
}

// boardLimitsCache holds what the runner last read from core.
type boardLimitsCache struct {
	mu       sync.Mutex
	val      *coreBoardLimits
	next     time.Time // when to read again
	fetching bool
	// fetch reads the limits from core; nil = core's internal endpoint. Tests set it.
	fetch func(ctx context.Context) (coreBoardLimits, error)
}

var errBoardLimitsUnavailable = errors.New("board limits could not be read from core")

// boardCallLimits is the per-minute and in-flight limits in force for a session's board calls: what
// core said last, else the runner's defaults. When the values are due for a read, one read starts in
// the background; this call does not wait for it.
func (a *API) boardCallLimits(ctx context.Context) (perMinute, inFlight int) {
	c := &a.boardLimits
	c.mu.Lock()
	val := c.val
	due := !c.fetching && !time.Now().Before(c.next) && (c.fetch != nil || (a.coreURL != "" && a.coreInternalKey != ""))
	if due {
		c.fetching = true
	}
	c.mu.Unlock()
	if due {
		go a.refreshBoardLimits(context.WithoutCancel(ctx)) // outlives the request that noticed it was due
	}
	if val == nil {
		return boardCallsPerMinute, boardCallsInFlight
	}
	return val.CallsPerMinute, val.CallsInFlight
}

// refreshBoardLimits reads the limits from core now, stores them and hands the unrevoked allowance
// to the gateway. A failure keeps the values in force and retries after boardLimitsRetry.
func (a *API) refreshBoardLimits(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := &a.boardLimits
	fetch := c.fetch
	if fetch == nil {
		fetch = a.fetchCoreBoardLimits
	}
	l, err := fetch(ctx)
	c.mu.Lock()
	c.fetching = false
	if err != nil || !l.valid() {
		c.next = time.Now().Add(boardLimitsRetry)
		c.mu.Unlock()
		log.Printf("board limits: could not read them from core; keeping the limits in force")
		return
	}
	changed := c.val == nil || *c.val != l
	c.val, c.next = &l, time.Now().Add(boardLimitsTTL)
	c.mu.Unlock()
	if g, ok := a.sessionBoardCaller().(interface{ SetUnrevokedBoardTokenLimit(int) }); ok {
		g.SetUnrevokedBoardTokenLimit(l.UnrevokedPerAccount)
	}
	if changed {
		log.Printf("board limits: %d requests a minute and %d calls at once per session, %d unrevoked credentials per account", l.CallsPerMinute, l.CallsInFlight, l.UnrevokedPerAccount)
	}
}

// fetchCoreBoardLimits is POST <core>/internal/board-limits with the runner's internal key.
func (a *API) fetchCoreBoardLimits(ctx context.Context) (coreBoardLimits, error) {
	if a.coreURL == "" || a.coreInternalKey == "" {
		return coreBoardLimits{}, errBoardLimitsUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/board-limits", bytes.NewReader([]byte("{}")))
	if err != nil {
		return coreBoardLimits{}, errBoardLimitsUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := coreHTTPClient(nil).Do(req)
	if err != nil {
		return coreBoardLimits{}, errBoardLimitsUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var out coreBoardLimits
	if err != nil || resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil {
		return coreBoardLimits{}, errBoardLimitsUnavailable
	}
	return out, nil
}
