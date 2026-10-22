package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The limits read the defaults until an administrator sets them; a set value is what an exchange is
// capped by, at the next exchange.
func TestBoardLimitsDefaultAndCapExchanges(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	admin := insertAccount(t, st, "limits-admin", "admin")
	acct := insertAccount(t, st, "limits-user", "member")

	got, err := svc.BoardLimits(ctx)
	if err != nil || got.Set || got.BoardLimits != DefaultBoardLimits {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	if DefaultBoardLimits.CallsPerMinute != 600 || DefaultBoardLimits.CallsInFlight != 8 ||
		DefaultBoardLimits.UnrevokedPerAccount != 20 || DefaultBoardLimits.LiveExchangeTokens != 100 {
		t.Fatalf("defaults moved: %+v", DefaultBoardLimits)
	}
	want := BoardLimits{CallsPerMinute: 900, CallsInFlight: 4, UnrevokedPerAccount: 5, LiveExchangeTokens: 12}
	if err := svc.SetBoardLimits(ctx, admin, want); err != nil {
		t.Fatal(err)
	}
	got, err = svc.BoardLimits(ctx)
	if err != nil || !got.Set || got.BoardLimits != want || got.UpdatedBy != admin || got.UpdatedAt == nil {
		t.Fatalf("after set: %+v %v", got, err)
	}
	for i := 0; i < want.LiveExchangeTokens; i++ {
		if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); err != nil {
			t.Fatalf("exchange #%d: %v", i, err)
		}
	}
	if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("exchange past the set cap: %v", err)
	}
	// Raised: the next exchange goes through, with no restart.
	want.LiveExchangeTokens = 13
	if err := svc.SetBoardLimits(ctx, admin, want); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); err != nil {
		t.Fatalf("exchange after the raise: %v", err)
	}
}

func TestBoardLimitsAreValidated(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	admin := insertAccount(t, st, "limits-admin-2", "admin")
	ok := DefaultBoardLimits
	for name, l := range map[string]BoardLimits{
		"zero per minute":       {CallsPerMinute: 0, CallsInFlight: 8, UnrevokedPerAccount: 20, LiveExchangeTokens: 100},
		"in flight above range": {CallsPerMinute: 600, CallsInFlight: 65, UnrevokedPerAccount: 20, LiveExchangeTokens: 100},
		"live tokens below 10":  {CallsPerMinute: 600, CallsInFlight: 8, UnrevokedPerAccount: 5, LiveExchangeTokens: 9},
		"unrevoked >= live":     {CallsPerMinute: 600, CallsInFlight: 8, UnrevokedPerAccount: 100, LiveExchangeTokens: 100},
		"in flight >= live":     {CallsPerMinute: 600, CallsInFlight: 20, UnrevokedPerAccount: 5, LiveExchangeTokens: 20},
	} {
		if err := svc.SetBoardLimits(ctx, admin, l); !errors.Is(err, ErrInvalidBoardLimits) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Several bad values: the first in the page's order is named, every time.
	for range 5 {
		err := svc.SetBoardLimits(ctx, admin, BoardLimits{})
		if err == nil || !strings.Contains(err.Error(), "calls_per_minute must be between") {
			t.Fatalf("all zero: %v", err)
		}
	}
	if got, _ := svc.BoardLimits(ctx); got.Set {
		t.Fatalf("a refused set was stored: %+v", got)
	}
	if err := svc.SetBoardLimits(ctx, admin, ok); err != nil {
		t.Fatal(err)
	}
}
