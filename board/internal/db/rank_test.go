package db_test

import (
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestRankInitial(t *testing.T) {
	k := db.RankInitial()
	if k == "" {
		t.Fatal("RankInitial() returned empty string")
	}
}

func TestRankBetween(t *testing.T) {
	cases := []struct {
		name    string
		prev    string
		next    string
		wantErr bool
	}{
		{
			name: "both_empty_returns_nonempty",
			prev: "", next: "",
		},
		{
			name: "no_upper_bound_returns_greater",
			prev: "h", next: "",
		},
		{
			name: "no_upper_bound_z",
			prev: "z", next: "",
		},
		{
			name: "no_lower_bound_returns_less",
			prev: "", next: "h",
		},
		{
			name: "no_lower_bound_b",
			prev: "", next: "b",
		},
		{
			name: "between_adjacent_single_chars",
			prev: "h", next: "j",
		},
		{
			name: "between_multi_char_keys",
			prev: "hh", next: "hz",
		},
		{
			name: "prev_equals_next_errors",
			prev: "h", next: "h",
			wantErr: true,
		},
		{
			name: "prev_greater_than_next_errors",
			prev: "z", next: "a",
			wantErr: true,
		},
		{
			name: "no_key_below_minimum_digit_errors",
			prev: "", next: "0",
			wantErr: true,
		},
		{
			name: "no_key_below_all_minimum_digits_errors",
			prev: "", next: "00",
			wantErr: true,
		},
		{
			name: "invalid_char_in_prev_errors",
			prev: "h!", next: "z",
			wantErr: true,
		},
		{
			name: "invalid_char_in_next_errors",
			prev: "a", next: "z~",
			wantErr: true,
		},
		{
			name: "between_adjacent_needing_extension",
			prev: "h", next: "i",
		},
		{
			name: "between_longer_keys",
			prev: "hh", next: "hi",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.RankBetween(tc.prev, tc.next)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RankBetween(%q, %q) expected error, got %q", tc.prev, tc.next, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RankBetween(%q, %q) unexpected error: %v", tc.prev, tc.next, err)
			}
			if got == "" {
				t.Fatalf("RankBetween(%q, %q) returned empty string", tc.prev, tc.next)
			}
			if tc.prev != "" && got <= tc.prev {
				t.Errorf("RankBetween(%q, %q) = %q; want > %q", tc.prev, tc.next, got, tc.prev)
			}
			if tc.next != "" && got >= tc.next {
				t.Errorf("RankBetween(%q, %q) = %q; want < %q", tc.prev, tc.next, got, tc.next)
			}
		})
	}
}

// TestRankBetweenNoKeyBelowMinReturnsFast verifies that the impossible
// "key below the minimum digit" request returns an error promptly rather than
// hanging in an infinite midpoint loop.
func TestRankBetweenNoKeyBelowMinReturnsFast(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := db.RankBetween("", "0"); err == nil {
			t.Errorf(`RankBetween("", "0") expected error, got nil`)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(`RankBetween("", "0") did not return within 2s — likely an infinite loop`)
	}
}

// TestRankBetweenRepeatedBisection verifies that 1000 repeated bisections between
// two initially adjacent keys stay strictly ordered with no collisions. It
// alternates collapsing the upper and lower halves so both extension paths are
// exercised.
func TestRankBetweenRepeatedBisection(t *testing.T) {
	const iterations = 1000

	// Start with two keys that differ by exactly one in their last character,
	// so the first bisection must extend the string. This is the stress case.
	lo := "h"
	hi := "i"

	seen := make(map[string]bool, iterations+2)
	seen[lo] = true
	seen[hi] = true

	for i := 0; i < iterations; i++ {
		mid, err := db.RankBetween(lo, hi)
		if err != nil {
			t.Fatalf("iteration %d: RankBetween(%q, %q) error: %v", i, lo, hi, err)
		}
		if mid == "" {
			t.Fatalf("iteration %d: RankBetween(%q, %q) returned empty", i, lo, hi)
		}
		if mid <= lo {
			t.Fatalf("iteration %d: got %q which is not > lo %q", i, mid, lo)
		}
		if mid >= hi {
			t.Fatalf("iteration %d: got %q which is not < hi %q", i, mid, hi)
		}
		if seen[mid] {
			t.Fatalf("iteration %d: collision — %q already seen", i, mid)
		}
		seen[mid] = true
		// Alternate which half we keep so we stress both the upper-bound
		// (hi = mid) and lower-bound (lo = mid) adjacent-extension paths.
		if i%2 == 0 {
			hi = mid
		} else {
			lo = mid
		}
	}
}
