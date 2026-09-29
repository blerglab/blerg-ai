package daemon

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// modelRe matches Claude Code's confirmation lines for /model:
//
//	"Model set to Sonnet 4.6"
//	"Kept model as Sonnet 4.6"
var modelRe = regexp.MustCompile(`(?i)(?:model set to|kept model as)\s+(.+)`)

// effortRe matches Claude Code's confirmation lines for /effort:
//
//	"Effort set to high"
//	"Kept effort as auto"
var effortRe = regexp.MustCompile(`(?i)(?:effort set to|kept effort as)\s+(.+)`)

// MetaParser watches PTY output for /model and /effort confirmation lines and
// emits session_meta_changed messages to the server when they are detected.
type MetaParser struct {
	sessionID string
	sender    Sender
	lineBuf   []byte
}

// NewMetaParser creates a MetaParser for the given session.
func NewMetaParser(sessionID string, sender Sender) *MetaParser {
	return &MetaParser{sessionID: sessionID, sender: sender}
}

// Process is called with each PTY output chunk.
func (p *MetaParser) Process(chunk []byte) {
	stripped := stripEscapes(chunk)
	p.lineBuf = append(p.lineBuf, stripped...)

	for {
		idx := bytes.IndexByte(p.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(p.lineBuf[:idx]))
		p.lineBuf = p.lineBuf[idx+1:]
		p.checkLine(line)
	}
	// Also check an unterminated last line in case the output doesn't end with \n.
	if len(p.lineBuf) > 0 {
		p.checkLine(strings.TrimSpace(string(p.lineBuf)))
	}
}

func (p *MetaParser) checkLine(line string) {
	if m := modelRe.FindStringSubmatch(line); len(m) == 2 {
		model := strings.TrimSpace(m[1])
		if model != "" {
			_ = p.sender.Send(protocol.SessionMetaChanged{
				Type:      "session_meta_changed",
				SessionID: p.sessionID,
				Model:     model,
			})
		}
	}
	if m := effortRe.FindStringSubmatch(line); len(m) == 2 {
		effort := strings.TrimSpace(m[1])
		if effort != "" {
			_ = p.sender.Send(protocol.SessionMetaChanged{
				Type:      "session_meta_changed",
				SessionID: p.sessionID,
				Effort:    effort,
			})
		}
	}
}
