package cardtime

import "testing"

// L9: ids and numbers only — a full ticket key with board context, the threshold reached and the
// round count. No worker titles, no session ids, no mark prose, no file or repository paths.
func TestNoticeTextNamesOnlyIDsAndNumbers(t *testing.T) {
	got := NoticeText("blg-board", 147, 4*3600, 3)
	want := "blg-board#147 has reached 4h of worker time across 3 review rounds."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoticeTextSingularRound(t *testing.T) {
	got := NoticeText("blg-board", 147, 8*3600, 1)
	want := "blg-board#147 has reached 8h of worker time across 1 review round."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoticeTextNoRoundsYet(t *testing.T) {
	got := NoticeText("blg-board", 147, 4*3600, 0)
	want := "blg-board#147 has reached 4h of worker time."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
