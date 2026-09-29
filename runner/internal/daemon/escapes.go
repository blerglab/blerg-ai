package daemon

import "regexp"

// ansiEscape matches ANSI CSI escape sequences like \x1b[31m, \x1b[0;1m,
// \x1b[?25l (private modes), etc. Used to strip styling from PTY output.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// oscEscape matches OSC sequences like \x1b]0;title\x07 (set terminal title),
// terminated by BEL or ST. Their payload (e.g. the window title) is not screen
// content.
var oscEscape = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// otherEscape matches charset-selection and other two-byte escapes (\x1b(B,
// \x1b)0, \x1b=, \x1b>, etc.) that carry no visible text.
var otherEscape = regexp.MustCompile(`\x1b[()][0-9A-Za-z]|\x1b[=>]`)

// stripEscapes removes ANSI/OSC/charset escape sequences from b, leaving the
// visible text.
func stripEscapes(b []byte) []byte {
	b = ansiEscape.ReplaceAll(b, nil)
	b = oscEscape.ReplaceAll(b, nil)
	b = otherEscape.ReplaceAll(b, nil)
	return b
}
