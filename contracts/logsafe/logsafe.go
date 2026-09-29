// Package logsafe keeps attacker-influenced values from forging log lines.
//
// The services log request-derived values (ids, paths, header values, error
// text from peers) with the standard library logger. A value carrying a line
// break would otherwise let a caller append what looks like a separate,
// genuine log entry. Install wraps the standard logger's output so that every
// entry is written as exactly one line: control characters inside the entry
// are replaced with a visible escape.
package logsafe

import (
	"fmt"
	"io"
	"log"
	"os"
	"unicode/utf8"
)

// Install routes the standard logger through a Writer over os.Stderr. Call
// it first thing in main, before anything logs.
func Install() {
	log.SetOutput(NewWriter(os.Stderr))
}

// Writer escapes control characters in each entry written to it. The log
// package hands a Writer one complete entry per Write call, ending in a
// newline; that final newline is the only one passed through.
type Writer struct {
	w io.Writer
}

// NewWriter returns a Writer that sanitises entries on their way to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write sanitises p and writes it as one line. It reports len(p) on success
// so callers see the write they asked for, not the escaped length.
func (s *Writer) Write(p []byte) (int, error) {
	if _, err := s.w.Write(Sanitize(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Sanitize returns entry with every control character escaped, except tabs
// and one trailing newline. Invalid UTF-8 bytes are escaped too. The input is
// returned as is when nothing needs escaping.
func Sanitize(entry []byte) []byte {
	body := entry
	trailing := false
	if n := len(body); n > 0 && body[n-1] == '\n' {
		body = body[:n-1]
		trailing = true
	}
	if clean(body) {
		return entry
	}
	out := make([]byte, 0, len(entry)+16)
	for len(body) > 0 {
		r, size := utf8.DecodeRune(body)
		switch {
		case r == utf8.RuneError && size == 1:
			out = fmt.Appendf(out, `\x%02x`, body[0])
		case r == '\n':
			out = append(out, `\n`...)
		case r == '\r':
			out = append(out, `\r`...)
		case unsafeRune(r):
			out = fmt.Appendf(out, `\u%04x`, r)
		default:
			out = append(out, body[:size]...)
		}
		body = body[size:]
	}
	if trailing {
		out = append(out, '\n')
	}
	return out
}

func clean(b []byte) bool {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			return false
		}
		b = b[size:]
	}
	return true
}

// unsafeRune reports whether r can break or disguise a log line: C0 and C1
// controls (tab excepted), the Unicode line and paragraph separators, and
// the bidirectional overrides that reorder text on display.
func unsafeRune(r rune) bool {
	switch {
	case r == '\t':
		return false
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x2028, r == 0x2029:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
