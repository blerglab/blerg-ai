package workbranch

import (
	"strings"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	c := Context{Username: "Alice Example", Session: "12345678-1234-1234-1234-123456789abc", Title: "Fix the Button", Date: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
	for _, tc := range []struct{ template, want string }{
		{"", "wip/fix-the-button-12345678"},
		{"wip/{session}", "wip/" + c.Session},
		{"dev/{username}/{slug}", "dev/alice-example/fix-the-button"},
		{"dev/{card}/{slug}", "dev/fix-the-button"},
		{"dev/{card}-{slug}", "dev/fix-the-button"},
		{"dev/{card}.{slug}", "dev/fix-the-button"},
		{"_work/{slug}--done_", "_work/fix-the-button--done_"},
		{"HEAD{card}", c.Session},
		{"{card}", c.Session},
		{"{date}/{session}", "2026-10-30/12345678"},
	} {
		got, err := Resolve(tc.template, c)
		if err != nil || got != tc.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", tc.template, got, err, tc.want)
		}
	}
	c.Card = "BLG-254"
	got, err := Resolve("dev/{card}/{slug}", c)
	if err != nil || got != "dev/blg-254/fix-the-button" {
		t.Fatalf("card = %q, %v", got, err)
	}
}

func TestValidate(t *testing.T) {
	for _, s := range []string{"HEAD", "-bad", "a..b", "a@{b", "a.lock", "a/.hidden", "a b", "a\\b", "a~b", "a:b", "a?b", "a[b", "a*b", "a/", "a//b", "a.", "@", "a\n"} {
		if Valid(s) {
			t.Errorf("accepted invalid branch %q", s)
		}
	}
	for _, s := range []string{"dev/person/fix", "wip/1234", "fix-button", "incoming.test"} {
		if !Valid(s) {
			t.Errorf("refused %q", s)
		}
	}
	if _, err := Resolve("", Context{Session: "bad..id"}); err == nil {
		t.Error("accepted invalid default branch")
	}
	for _, s := range []string{"dev/{typo}", "dev/{slug", "dev/../{session}", "dev/{session}.lock"} {
		if _, err := Resolve(s, Context{Session: "12345678"}); err == nil {
			t.Errorf("accepted template %q", s)
		}
	}
}

func TestReadableDefaults(t *testing.T) {
	for _, title := range []string{"", "!!!", "日本語", "Fix ../refs @{bad}.lock", strings.Repeat("Long title ", 100)} {
		got, err := Resolve("", Context{Session: "abcdef01-2345-6789-abcd-0123456789ab", Title: title})
		if err != nil || !Valid(got) || len(got) > 80 {
			t.Errorf("title %q: %q, %v", title, got, err)
		}
	}
	got, err := Resolve("", Context{Session: "abcdef01-2345-6789-abcd-0123456789ab"})
	if err != nil || got != "wip/session-abcdef01" {
		t.Fatalf("untitled: %q, %v", got, err)
	}
	if got := Effective("old-id", ""); got != "wip/old-id" {
		t.Fatal(got)
	}
	if got := Effective("old-id", "wip/saved-name-abcd"); got != "wip/saved-name-abcd" {
		t.Fatal(got)
	}
}
