package server

// The session board call limits follow core's settings (board_limits.go): the runner reads them with
// its internal key, uses them at the next call, hands the unrevoked allowance to the gateway, and
// keeps its defaults (or the last values read) when core cannot be asked.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// limitedBoard is the fake board that also takes the unrevoked allowance, as the gateway does.
type limitedBoard struct {
	*fakeSessionBoard
	mu        sync.Mutex
	unrevoked int
}

func (b *limitedBoard) SetUnrevokedBoardTokenLimit(n int) {
	b.mu.Lock()
	b.unrevoked = n
	b.mu.Unlock()
}

func TestBoardLimitDefaultsAreRaised(t *testing.T) {
	if boardCallsPerMinute != 600 || boardCallsInFlight != 8 {
		t.Fatalf("defaults %d a minute, %d at once", boardCallsPerMinute, boardCallsInFlight)
	}
	a := &API{}
	if per, inFlight := a.boardCallLimits(context.Background()); per != 600 || inFlight != 8 {
		t.Fatalf("without core: %d, %d", per, inFlight)
	}
}

func TestBoardLimitsFollowCoresSettings(t *testing.T) {
	fx, fb, _ := newBoardLevelFx(t)
	lb := &limitedBoard{fakeSessionBoard: fb}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: lb})
	var mu sync.Mutex
	set, fail := coreBoardLimits{CallsPerMinute: 2, CallsInFlight: 1, UnrevokedPerAccount: 5}, false
	fx.api.boardLimits.fetch = func(context.Context) (coreBoardLimits, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return coreBoardLimits{}, errors.New("core unavailable")
		}
		return set, nil
	}
	fx.api.refreshBoardLimits(context.Background())
	if per, inFlight := fx.api.boardCallLimits(context.Background()); per != 2 || inFlight != 1 {
		t.Fatalf("after the read: %d, %d", per, inFlight)
	}
	if lb.unrevoked != 5 {
		t.Fatalf("gateway unrevoked allowance %d", lb.unrevoked)
	}
	boardCallLimit.Lock()
	delete(boardCallLimit.m, fx.sess)
	boardCallLimit.Unlock()
	access := func() int {
		return filingRequest(fx, http.MethodGet, "/api/sessions/"+fx.sess+"/board-access", fx.sessTok, nil).Code
	}
	if a, b, c := access(), access(), access(); a != 200 || b != 200 || c != http.StatusTooManyRequests {
		t.Fatalf("per-minute limit of 2: %d %d %d", a, b, c)
	}
	// One call at once: a second waits, then is refused, naming the limit.
	release, ok := acquireBoardCall(context.Background(), fx.sess, 1)
	if !ok {
		t.Fatal("first slot")
	}
	old := boardCallWait
	boardCallWait = 50 * time.Millisecond
	t.Cleanup(func() { boardCallWait = old })
	boardCallLimit.Lock()
	delete(boardCallLimit.m, fx.sess)
	boardCallLimit.Unlock()
	setLevel(fx, fx.token(topicOwner, "human"), "board-a", db.BoardLevelRead)
	boardCallLimit.Lock()
	delete(boardCallLimit.m, fx.sess)
	boardCallLimit.Unlock()
	code, body := boardTool(fx, fx.sessTok, "board-a", "blerg_card_get", nil)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "at most 1 run together") {
		t.Fatalf("second call at once: %d %s", code, body)
	}
	// A raised limit holds at the next call, the running one still in flight.
	mu.Lock()
	set = coreBoardLimits{CallsPerMinute: 100, CallsInFlight: 2, UnrevokedPerAccount: 7}
	mu.Unlock()
	fx.api.refreshBoardLimits(context.Background())
	if code, body := boardTool(fx, fx.sessTok, "board-a", "blerg_card_get", nil); code != 200 {
		t.Fatalf("after raising to 2 at once: %d %s", code, body)
	}
	release()
	// Core cannot be asked: the last values read stay in force.
	mu.Lock()
	fail = true
	mu.Unlock()
	fx.api.refreshBoardLimits(context.Background())
	if per, inFlight := fx.api.boardCallLimits(context.Background()); per != 100 || inFlight != 2 || lb.unrevoked != 7 {
		t.Fatalf("after a failed read: %d, %d, unrevoked %d", per, inFlight, lb.unrevoked)
	}
	// Out-of-range values from core are not taken.
	mu.Lock()
	fail, set = false, coreBoardLimits{CallsPerMinute: 0, CallsInFlight: 2, UnrevokedPerAccount: 7}
	mu.Unlock()
	fx.api.refreshBoardLimits(context.Background())
	if per, _ := fx.api.boardCallLimits(context.Background()); per != 100 {
		t.Fatalf("an invalid read was taken: %d", per)
	}
}

// The runner asks core's internal route with its internal key, and is re-read only after the TTL.
func TestBoardLimitsAreReadFromCoreWithTheInternalKey(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/internal/board-limits" || r.Header.Get("X-Internal-Key") != "fixture-internal-key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"calls_per_minute": 900, "calls_in_flight": 12, "unrevoked_per_account": 30, "live_exchange_tokens": 200})
	}))
	t.Cleanup(core.Close)
	a := &API{hub: NewHub(), coreURL: core.URL, coreInternalKey: "fixture-internal-key"}
	got, err := a.fetchCoreBoardLimits(context.Background())
	if err != nil || got != (coreBoardLimits{CallsPerMinute: 900, CallsInFlight: 12, UnrevokedPerAccount: 30}) {
		t.Fatalf("fetch: %+v %v", got, err)
	}
	// The first use starts a read in the background and answers the defaults meanwhile.
	if per, inFlight := a.boardCallLimits(context.Background()); per != boardCallsPerMinute || inFlight != boardCallsInFlight {
		t.Fatalf("before the read: %d, %d", per, inFlight)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if per, inFlight := a.boardCallLimits(context.Background()); per == 900 && inFlight == 12 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background read never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	n := hits
	mu.Unlock()
	for range 20 {
		a.boardCallLimits(context.Background())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != n {
		t.Fatalf("core asked again within the TTL: %d -> %d", n, hits)
	}
	a.coreInternalKey = "wrong"
	if _, err := a.fetchCoreBoardLimits(context.Background()); err == nil {
		t.Fatal("a refused read was taken")
	}
}
