package db

import (
	"fmt"
	"strings"
)

// rankAlphabet is the ordered set of characters used in rank keys.
// Keys are compared using Go's < operator (lexicographic / byte order), so the
// alphabet must be in strictly ascending byte-value order.
const rankAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
const rankBase = len(rankAlphabet) // 36

// RankInitial returns a starting rank key. Chosen near the middle of the
// alphabet so there is equal room to prepend or append new keys.
func RankInitial() string {
	return string(rankAlphabet[rankBase/2]) // "h" (index 17)
}

// RankBetween returns a rank key that is strictly ordered (by Go's < string
// comparison) between prev and next. An empty string means "no bound" on that
// side:
//   - RankBetween("", "")  → a valid initial key (same as RankInitial)
//   - RankBetween(a, "")   → a key strictly greater than a
//   - RankBetween("", b)   → a key strictly less than b
//   - RankBetween(a, b)    → a key strictly between a and b
//
// Returns an error when both bounds are non-empty and prev >= next, or when a
// key contains characters outside the rank alphabet.
func RankBetween(prev, next string) (string, error) {
	// Fast-path: neither bound.
	if prev == "" && next == "" {
		return RankInitial(), nil
	}

	// Validate and reject out-of-order bounds.
	if prev != "" && next != "" && prev >= next {
		return "", fmt.Errorf("rank: prev %q >= next %q", prev, next)
	}

	if err := rankValidate(prev); err != nil {
		return "", err
	}
	if err := rankValidate(next); err != nil {
		return "", err
	}

	var result string

	switch {
	case next == "":
		// No upper bound: appending the mid-alphabet character always produces a
		// string that is lexicographically greater than prev.
		result = prev + string(rankAlphabet[rankBase/2])

	case prev == "":
		// No lower bound: find the midpoint between "all zeros" and next. When
		// next is composed entirely of the minimum digit (e.g. "0", "00"), no
		// key is strictly less than it — reject rather than loop.
		mid, ok := rankMidBetween(nil, rankParseDigits(next))
		if !ok {
			return "", fmt.Errorf("rank: no key exists below %q", next)
		}
		result = rankDigitsToStr(mid)

	default:
		mid, ok := rankMidBetween(rankParseDigits(prev), rankParseDigits(next))
		if !ok {
			return "", fmt.Errorf("rank: no key exists between %q and %q", prev, next)
		}
		result = rankDigitsToStr(mid)
	}

	// Defensive sanity checks (should never fire on valid input).
	if prev != "" && result <= prev {
		return "", fmt.Errorf("rank: internal error: computed %q is not > %q", result, prev)
	}
	if next != "" && result >= next {
		return "", fmt.Errorf("rank: internal error: computed %q is not < %q", result, next)
	}

	return result, nil
}

// rankValidate returns an error if s contains any character outside rankAlphabet.
// The empty string is always valid (it is the sentinel for "no bound").
func rankValidate(s string) error {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(rankAlphabet, s[i]) < 0 {
			return fmt.Errorf("rank: invalid character %q in key %q", s[i], s)
		}
	}
	return nil
}

// rankParseDigits converts a rank key string into a slice of digit values
// (each in [0, rankBase)).
func rankParseDigits(s string) []int {
	digits := make([]int, len(s))
	for i := 0; i < len(s); i++ {
		digits[i] = strings.IndexByte(rankAlphabet, s[i])
	}
	return digits
}

// rankDigitsToStr converts a digit slice back into a rank key string.
func rankDigitsToStr(digits []int) string {
	b := make([]byte, len(digits))
	for i, d := range digits {
		b[i] = rankAlphabet[d]
	}
	return string(b)
}

// rankMidBetween computes a digit slice that is strictly between lo and hi.
// It returns ok == false when no such slice exists (lo and hi are effectively
// equal — e.g. an unbounded lower end against an all-zeros upper bound), so the
// caller can report an error instead of looping.
//
// lo == nil means "conceptual minimum" (all zeros, extended infinitely).
// hi == nil means "conceptual maximum" (all (rankBase-1), extended infinitely).
//
// Invariant: lo < hi when both are non-nil (the caller guarantees this).
//
// Algorithm (LexoRank-style):
//  1. Walk digit positions left-to-right, consuming from lo and hi.
//  2. While the digits at the current position are equal, emit that digit and
//     advance (they must agree on the common prefix). If both sides are
//     exhausted while still equal, lo == hi and no midpoint exists → ok=false.
//  3. At the first position where they differ (dLo < dHi):
//     a. If dHi−dLo ≥ 2: emit (dLo+dHi)/2 and return.
//     b. If dHi−dLo == 1: emit dLo, then recurse with the remainder of lo
//     against an unbounded upper end (hi = nil). This is equivalent to
//     finding the midpoint between lo_tail and "zzzz…" — always in
//     (lo_tail, hi_tail_as_zero_extension) ⊆ (lo, hi).
//
// Termination: each iteration of the equal-digit branch consumes a real digit
// from lo or hi (or detects mutual exhaustion and returns), so the loop runs at
// most max(len(lo), len(hi))+1 times. The single recursion uses hi == nil,
// whose sentinel rankBase-1 differs from any lo digit by ≥ 2 (rankBase = 36),
// so it returns on its first differing position. There is no unbounded loop.
func rankMidBetween(lo, hi []int) ([]int, bool) {
	result := make([]int, 0, max(len(lo), len(hi))+2)
	loIdx, hiIdx := 0, 0

	for {
		// Current digit of lo (extend with 0 when exhausted).
		dLo := 0
		loExhausted := loIdx >= len(lo)
		if !loExhausted {
			dLo = lo[loIdx]
			loIdx++
		}

		// Current digit of hi (extend with rankBase-1 when nil/unbounded,
		// extend with 0 when exhausted but non-nil).
		var dHi int
		hiExhausted := false
		switch {
		case hi == nil:
			dHi = rankBase - 1
		case hiIdx < len(hi):
			dHi = hi[hiIdx]
			hiIdx++
		default:
			dHi = 0
			hiExhausted = true
		}

		if dLo == dHi {
			// Both sides exhausted and still equal: lo and hi are identical when
			// zero-extended, so nothing lies strictly between them.
			if loExhausted && hiExhausted {
				return nil, false
			}
			// Digits agree at this position; emit and keep going.
			result = append(result, dLo)
			continue
		}

		if dHi < dLo {
			// lo < hi is guaranteed by the caller for valid input; reaching here
			// means that invariant was violated. Fail loudly rather than emit a
			// silently out-of-order key.
			panic("rank: invariant violated: dHi < dLo")
		}

		// dLo < dHi.
		if dHi-dLo >= 2 {
			result = append(result, (dLo+dHi)/2)
			return result, true
		}

		// dHi == dLo+1: gap of exactly 1 — can't place a midpoint in this
		// digit position. Commit to dLo and recurse into the tail of lo
		// against an unbounded upper end.
		result = append(result, dLo)
		loRest := lo[loIdx:] // remaining lo digits after the current position
		tail, ok := rankMidBetween(loRest, nil)
		if !ok {
			return nil, false
		}
		result = append(result, tail...)
		return result, true
	}
}
