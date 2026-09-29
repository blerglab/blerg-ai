package logsafe

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain entry untouched", "2026/01/01 listening on :8080\n", "2026/01/01 listening on :8080\n"},
		{"tab kept", "a\tb\n", "a\tb\n"},
		{"non-ascii kept", "na\u00efve \u2014 ok\n", "na\u00efve \u2014 ok\n"},
		{"forged line", "remove abc\n2026/01/01 admin granted\n", `remove abc\n2026/01/01 admin granted` + "\n"},
		{"carriage return", "a\rb\n", `a\rb` + "\n"},
		{"escape sequence", "a\x1b[2Jb\n", `a\u001b[2Jb` + "\n"},
		{"bell and nul", "a\x07\x00\n", `a\u0007\u0000` + "\n"},
		{"bidi override", "a\u202eb\n", `a\u202eb` + "\n"},
		{"line separator", "a\u2028b\n", `a\u2028b` + "\n"},
		{"invalid utf8", "a\xffb\n", `a\xffb` + "\n"},
		{"no trailing newline", "a\nb", `a\nb`},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Sanitize([]byte(c.in))); got != c.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestWriterKeepsOneLinePerEntry(t *testing.T) {
	var buf bytes.Buffer
	l := log.New(NewWriter(&buf), "", 0)
	l.Printf("remove %s: not found", "abc\n2026/01/01 forged entry")
	l.Printf("second")
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %d: %q", n, buf.String())
	}
}

func TestWriterReportsInputLength(t *testing.T) {
	var buf bytes.Buffer
	in := []byte("a\nb\n")
	n, err := NewWriter(&buf).Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriterPropagatesError(t *testing.T) {
	if _, err := NewWriter(failWriter{}).Write([]byte("x\n")); err == nil {
		t.Fatal("want error")
	}
}
