package server

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"golang.org/x/text/unicode/norm"
)

// personOnlyQuestion reports a question only the person resolves, at any depth: ask --person, a
// command approval, an escalation, or one whose waiter already ended. The session side may stop
// waiting on it but never closes it (holdPersonQuestion).
func personOnlyQuestion(m db.MessageRow) bool {
	return m.PersonRequired || m.ChildEscalated || m.WaiterEnded || (m.OwnerOnly && !m.NoWait)
}

// questionNeedsPerson reports a question only a person may answer: a person-only one, or one that
// is owner-only. Anything else a session's chat can answer.
func questionNeedsPerson(m db.MessageRow) bool {
	return m.OwnerOnly || personOnlyQuestion(m)
}

// Questions are prose, not shell programs. This best-effort list recognizes only
// unmistakable destructive phrases. Guidance and ask --person cover other cases.
var destructiveQuestionPhrase = regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:force[\s-]+push|git\s+push\s+[^\n?]*--force(?:-with-lease)?|reset\s+--hard|rm\s+-(?:rf|fr)|drop\s+(?:the\s+)?(?:table|database)|truncate\s+table|delete\s+(?:the\s+)?(?:production(?:\s+database)?|repo(?:sitory)?)|gh\s+repo\s+delete|mkfs(?:\.[a-z0-9]+)?|git\s+branch\s+-D)(?:$|[^\pL\pN_])`)

func destructiveQuestion(body string) bool {
	body = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, norm.NFKC.String(body))
	return destructiveQuestionPhrase.MatchString(body)
}

// requiresPerson keeps phrase classification and request flags in one routing
// decision. Persistence and delivery consume this decision without reclassifying.
func (req postMessageRequest) requiresPerson(coordinator bool) bool {
	return req.Kind == "ask" && (req.Urgent || req.PersonRequired || (req.OwnerOnly && !req.NoWait) || (coordinator && destructiveQuestion(req.Body)))
}
