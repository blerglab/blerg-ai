package server

// The visible failure (AI crons spec 10.4). A cron run that cannot start, and whose cron targets a
// board, posts ONE card there ("Cron <name> could not run: <reason>"). The card has a fixed external
// id per cron, so the board updates it in place on every repeat instead of filing a new one.
//
// It is best effort and never part of the run: it is posted from a goroutine with its own deadline,
// a failure only logs, and nothing about it can fail, hold or slow the scheduler. No card is tried
// when the failure IS the board (the exchange or the address), because the card would need the same
// exchange; nor when no proof remains to post with (a revoked or expired cron token).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const (
	cardReasonMax = 200
	cardNameMax   = 60
	cardPostTime  = 20 * time.Second
	// A cron posts at most one card per cardPerCronGap, and at most cardMaxConcurrent posts are in
	// flight across every cron, so a cron failing every minute (or many at once) cannot pile up
	// goroutines, board tokens and requests.
	cardPerCronGap    = 5 * time.Minute
	cardMaxConcurrent = 4
	// A board gate's answer (held, stale, rejected) is logged at most once per cron per this long.
	cardGateLogEvery = time.Hour
)

// CronFailureCard is the card a failed run leaves on its board.
type CronFailureCard struct {
	ExternalID string
	Title      string
	Body       string
}

// CronBoardCards posts a failure card. The real one exchanges a board token with the proof, posts to
// the board's REST API and revokes the token; tests use a fake.
type CronBoardCards interface {
	PostCard(ctx context.Context, proof mcpgw.Proof, boardID string, card CronFailureCard) error
}

// cronFailureExternalID is the card's fixed external id: one card per cron, updated in place.
func cronFailureExternalID(cronID string) string { return "cron-failure:" + cronID }

// newCronFailureCard builds the card for a run of c that failed with reason (already sanitised).
func newCronFailureCard(c *db.Cron, run *db.CronRun, reason string) CronFailureCard {
	name := sanitizeCardName(c.Name)
	slot := ""
	if run != nil {
		slot = fmt.Sprintf("The run scheduled for %s UTC did not start.\n\n", run.ScheduledFor.UTC().Format("2006-01-02 15:04"))
	}
	return CronFailureCard{
		ExternalID: cronFailureExternalID(c.ID),
		Title:      "Cron " + name + " could not run: " + reason,
		Body: slot + "Reason: " + reason + "\n\nFix the cause in the runner (the cron's page) and the next scheduled run goes ahead. " +
			"This card is updated in place each time a run fails to start.",
	}
}

var (
	cardURLRe    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	cardIPv6Re   = regexp.MustCompile(`\[[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*\](:\d{1,5})?`)
	cardIPv4Re   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{1,5})?\b`)
	cardHostPort = regexp.MustCompile(`\b[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*:\d{2,5}\b`)
	cardHostRe   = regexp.MustCompile(`\b(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}\b`)
	cardJWTRe    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	cardBearerRe = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+\S+`)
	cardLongRe   = regexp.MustCompile(`[A-Za-z0-9_+/=.-]{32,}`)
	cardSpaceRe  = regexp.MustCompile(`\s+`)
)

// sanitizeCardReason turns an error into text safe to put on a board: the first line only, with
// addresses, bearer tokens and long token-like strings replaced, control characters gone, and a
// length cap. The reasons the start path produces are its own sentences; this is the guard for the
// ones that quote something from outside (an upstream's error, a core message).
func sanitizeCardReason(reason string) string {
	for line := range strings.SplitSeq(reason, "\n") {
		if strings.TrimSpace(line) != "" {
			reason = line
			break
		}
	}
	reason = cardURLRe.ReplaceAllString(reason, "[address]")
	// Bare addresses too: "dial tcp 192.0.2.5:8080", "[fe80::1]:443", "core.internal:8080", "host.example".
	reason = cardIPv6Re.ReplaceAllString(reason, "[address]")
	reason = cardIPv4Re.ReplaceAllString(reason, "[address]")
	reason = cardHostPort.ReplaceAllString(reason, "[address]")
	reason = cardHostRe.ReplaceAllString(reason, "[address]")
	reason = cardJWTRe.ReplaceAllString(reason, "[redacted]")
	reason = cardBearerRe.ReplaceAllString(reason, "$1 [redacted]")
	reason = cardLongRe.ReplaceAllString(reason, "[redacted]")
	reason = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason)
	reason = strings.TrimSpace(cardSpaceRe.ReplaceAllString(reason, " "))
	if reason == "" {
		return "unknown reason"
	}
	if r := []rune(reason); len(r) > cardReasonMax {
		reason = string(r[:cardReasonMax]) + "…"
	}
	return reason
}

// sanitizeCardName makes a cron's name safe to put in a card's title and body: control characters
// become spaces, the characters that are markup on a board (emphasis, links, headings, tables,
// HTML, mentions, escapes) are dropped, whitespace is collapsed and the length is capped.
func sanitizeCardName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\u2028' || r == '\u2029' || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e):
			return ' '
		case strings.ContainsRune("`*_~[]()<>#|!\\&@{}=:", r):
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(cardSpaceRe.ReplaceAllString(name, " "))
	if name == "" {
		return "(unnamed)"
	}
	if r := []rune(name); len(r) > cardNameMax {
		name = string(r[:cardNameMax]) + "…"
	}
	return name
}

// cardReasonOf is the reason a failed start is announced with. The reasons the start path writes
// itself (a cronError: a connection that needs attention, no credential) are sentences of ours and
// pass through the sanitiser; anything else is one of a small fixed vocabulary, so the text of a
// network or library error (which names hosts, ports and paths) never reaches a board.
func cardReasonOf(cause error) string {
	var ce *cronError
	var ne net.Error
	var ue *url.Error
	switch {
	case errors.As(cause, &ce):
		return sanitizeCardReason(ce.msg)
	case errors.Is(cause, errDaemonUnavailable):
		return "no suitable daemon is connected"
	case errors.As(cause, &ne), errors.As(cause, &ue):
		return "a service the run needs could not be reached"
	}
	return "the run could not be started (see the runner's logs)"
}

// cardLimiter bounds the failure cards: a per-cron minimum gap and a global cap on posts in flight.
// The zero value is ready to use.
type cardLimiter struct {
	mu       sync.Mutex
	last     map[string]time.Time
	gateLog  map[string]time.Time
	inflight int
}

// admit reports whether a post for cronID may start now, and if so records it. false: the cron
// posted within cardPerCronGap, or cardMaxConcurrent posts are in flight.
func (l *cardLimiter) admit(cronID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight >= cardMaxConcurrent {
		return false
	}
	if t, ok := l.last[cronID]; ok && now.Sub(t) < cardPerCronGap {
		return false
	}
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if len(l.last) > 1000 {
		for k, t := range l.last {
			if now.Sub(t) >= cardPerCronGap {
				delete(l.last, k)
			}
		}
	}
	l.last[cronID] = now
	l.inflight++
	return true
}

func (l *cardLimiter) done() {
	l.mu.Lock()
	l.inflight--
	l.mu.Unlock()
}

// shouldLogGate reports whether a gate answer for cronID should be logged now (once per hour).
func (l *cardLimiter) shouldLogGate(cronID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.gateLog[cronID]; ok && now.Sub(t) < cardGateLogEvery {
		return false
	}
	if l.gateLog == nil {
		l.gateLog = map[string]time.Time{}
	}
	if len(l.gateLog) > 1000 {
		for k, t := range l.gateLog {
			if now.Sub(t) >= cardGateLogEvery {
				delete(l.gateLog, k)
			}
		}
	}
	l.gateLog[cronID] = now
	return true
}

// cardGateError is the board's gate answering the failure card with something other than "posted":
// held for review, refused, or a conflict. It is the board's policy talking, not an outage.
type cardGateError struct{ Status int }

func (e *cardGateError) Error() string {
	return fmt.Sprintf("the board's gate answered %d to the failure card (held, refused or conflicting)", e.Status)
}

// sessionStartError marks a failure of StartSession itself. Past that point a session may exist or
// come to exist (the scheduler leaves such runs to its reaper), so it is not reported as "could not
// run". A credential failure from it still is.
type sessionStartError struct{ err error }

func (e *sessionStartError) Error() string { return e.err.Error() }
func (e *sessionStartError) Unwrap() error { return e.err }

// failureCardWorthy reports whether a failed start is one a card should announce.
func failureCardWorthy(err error) bool {
	var board *boardAccessError
	var started *sessionStartError
	switch {
	case err == nil,
		errors.Is(err, cron.ErrNotRunnable), errors.Is(err, errAccessRevoked), // no proof left to post with
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &board): // the board itself is the problem
		return false
	case errors.Is(err, cron.ErrCredential):
		return true
	case errors.Is(err, cron.ErrCapacity): // held and retried, not failed
		return false
	case errors.As(err, &started):
		return false
	}
	return true
}

// postFailureCard posts the card for a failed start in the background. It returns at once. A cron
// posts at most one card per cardPerCronGap (a cron failing every minute is one card, refreshed, not
// a request per failure) and at most cardMaxConcurrent posts run at a time; a failure over either
// limit is not announced again until it is allowed.
func (s *CronService) postFailureCard(ctx context.Context, c *db.Cron, run *db.CronRun, cause error) {
	if s.cards == nil || c.BoardID == nil || *c.BoardID == "" || !failureCardWorthy(cause) {
		return
	}
	if !s.cardGate.admit(c.ID, s.cfg.Now()) {
		return
	}
	card := newCronFailureCard(c, run, cardReasonOf(cause))
	proof := mcpgw.Proof{AccountID: c.OwnerAccountID, TokenID: c.TokenID}
	boardID, cronID := *c.BoardID, c.ID
	bg := context.WithoutCancel(ctx)
	s.cardWG.Add(1)
	go func() {
		defer s.cardWG.Done()
		defer s.cardGate.done()
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Logf("cron %s: failure card: panic: %v", cronID, r)
			}
		}()
		pctx, cancel := context.WithTimeout(bg, cardPostTime)
		defer cancel()
		if err := s.cards.PostCard(pctx, proof, boardID, card); err != nil {
			var gate *cardGateError
			if errors.As(err, &gate) {
				if s.cardGate.shouldLogGate(cronID, s.cfg.Now()) {
					s.cfg.Logf("cron %s: failure card not posted, the board's gate answered %d (held for review, refused or conflicting); logged once an hour", cronID, gate.Status)
				}
				return
			}
			s.cfg.Logf("cron %s: failure card not posted: %v", cronID, err)
		}
	}()
}

// WaitCards blocks until the failure cards started so far have finished (tests, shutdown).
func (s *CronService) WaitCards() { s.cardWG.Wait() }

// ---- the real poster -------------------------------------------------------------------------

// HTTPBoardCards posts the card to the board's REST API (POST /api/boards/{id}/cards, which upserts
// on external_id) with a board token exchanged for the cron, and revokes the token afterwards.
//
// An existing card that was archived, or moved to a terminal column, would be refreshed out of
// sight, so before posting it looks the card up and, when it is archived or done, asks the board to
// put it back in the board's first open column (the board unarchives a card moved that way).
type HTTPBoardCards struct {
	BaseURL   string // the board's base address (BLERG_RUNNER_BOARD_URL)
	Exchanger mcpgw.BoardExchanger
	HTTP      *http.Client // nil: mcpgw.BoardHTTPClient(BaseURL), which reaches only the board's host

	once   sync.Once
	client *http.Client
}

// httpClient is the client every post uses, built once (a netguard client per post would rebuild
// its transport, and leak its idle connections, each time).
func (h *HTTPBoardCards) httpClient() *http.Client {
	h.once.Do(func() {
		h.client = h.HTTP
		if h.client == nil {
			h.client = mcpgw.BoardHTTPClient(h.BaseURL, cardPostTime, 1<<20)
		}
	})
	return h.client
}

type boardCardBody struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	ExternalID string `json:"external_id"`
	ColumnID   string `json:"column_id,omitempty"`
}

// getJSON GETs a board path with the token and decodes the JSON reply into out. Any failure is an
// error; callers treat the lookups as best effort.
func (h *HTTPBoardCards) getJSON(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+path, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("the board answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// reopenColumn returns the column the card must be moved to so it is visible again: "" when the card
// does not exist yet, is live in an open column, or cannot be looked up (best effort: a failed lookup
// posts as before).
func (h *HTTPBoardCards) reopenColumn(ctx context.Context, token, boardID, externalID string) string {
	base := "/api/boards/" + url.PathEscape(boardID)
	type boardCard struct {
		ColumnID   *string `json:"column_id"`
		ExternalID *string `json:"external_id"`
	}
	find := func(list []boardCard) *boardCard {
		for i := range list {
			if list[i].ExternalID != nil && *list[i].ExternalID == externalID {
				return &list[i]
			}
		}
		return nil
	}
	var cols []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}
	if err := h.getJSON(ctx, token, base+"/columns", &cols); err != nil {
		return ""
	}
	isDone := func(id string) bool {
		for _, c := range cols {
			if c.ID == id {
				return c.IsTerminal || strings.Contains(strings.ToLower(c.Name), "done")
			}
		}
		return false
	}
	open := ""
	for _, c := range cols {
		if !isDone(c.ID) {
			open = c.ID
			break
		}
	}
	if open == "" {
		return ""
	}
	var live []boardCard
	if err := h.getJSON(ctx, token, base+"/cards", &live); err != nil {
		return ""
	}
	if existing := find(live); existing != nil {
		if existing.ColumnID != nil && isDone(*existing.ColumnID) {
			return open // finished: bring it back
		}
		return "" // live in an open column: updated in place
	}
	var archived []boardCard
	if err := h.getJSON(ctx, token, base+"/cards?archived=1", &archived); err != nil {
		return ""
	}
	if find(archived) != nil {
		return open // archived: moving it to a column unarchives it
	}
	return ""
}

// PostCard implements CronBoardCards.
func (h *HTTPBoardCards) PostCard(ctx context.Context, proof mcpgw.Proof, boardID string, card CronFailureCard) error {
	if h.BaseURL == "" || h.Exchanger == nil {
		return errors.New("the board is not configured")
	}
	tok, err := h.Exchanger.Exchange(ctx, proof, boardID, "")
	if err != nil {
		return fmt.Errorf("board token exchange: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = h.Exchanger.Revoke(rctx, proof.AccountID, tok.Sub) // expires in 10 minutes anyway
	}()
	raw, err := json.Marshal(boardCardBody{
		Title: card.Title, Body: card.Body, ExternalID: card.ExternalID,
		ColumnID: h.reopenColumn(ctx, tok.Token, boardID, card.ExternalID),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(h.BaseURL, "/")+"/api/boards/"+url.PathEscape(boardID)+"/cards", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := h.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("board unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusAccepted, http.StatusConflict, http.StatusUnprocessableEntity:
		return &cardGateError{Status: resp.StatusCode}
	}
	return fmt.Errorf("the board answered %d to the failure card", resp.StatusCode)
}
