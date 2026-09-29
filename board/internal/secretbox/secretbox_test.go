package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	b, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTripAndFreshNonce(t *testing.T) {
	b := newBox(t)
	c1, err := b.Seal("s3cret-token", "board-1")
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := b.Seal("s3cret-token", "board-1")
	if c1 == c2 {
		t.Fatal("two seals of the same plaintext are identical: nonce reused")
	}
	if !IsSealed(c1) || strings.Contains(c1, "s3cret") {
		t.Fatalf("bad sealed form %q", c1)
	}
	got, err := b.Open(c1, "board-1")
	if err != nil || got != "s3cret-token" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestWrongKeyCorruptionAndBoundAD(t *testing.T) {
	b := newBox(t)
	other := newBox(t)
	c, _ := b.Seal("tok", "board-1")

	if _, err := other.Open(c, "board-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := b.Open(c, "board-2"); !errors.Is(err, ErrOpen) {
		t.Fatalf("ciphertext moved to another board opened: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(c, V1Prefix))
	raw[len(raw)-1] ^= 1
	if _, err := b.Open(V1Prefix+base64.StdEncoding.EncodeToString(raw), "board-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("flipped bit: %v", err)
	}
	for _, bad := range []string{"", "plaintext", "v1:", "v1:!!!", "v1:AAAA", "v2:" + strings.TrimPrefix(c, V1Prefix)} {
		if _, err := b.Open(bad, "board-1"); !errors.Is(err, ErrOpen) {
			t.Fatalf("Open(%q): %v", bad, err)
		}
	}
}

func TestParseKeyStrict(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if k, err := ParseKey(good + "\n"); err != nil || len(k) != 32 {
		t.Fatalf("good key: %v", err)
	}
	for _, bad := range []string{
		"", "   ", "short", strings.Repeat("ab", 32), // hex is not accepted
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16)),
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 33)),
		strings.TrimRight(good, "="), // unpadded
	} {
		_, err := ParseKey(bad)
		if err == nil {
			t.Fatalf("ParseKey(%q) accepted", bad)
		}
		if strings.TrimSpace(bad) != "" && strings.Contains(err.Error(), bad) {
			t.Fatalf("error echoes the key: %v", err)
		}
	}
}
