You are working card #{{.Card.Number}} on the blerg-board board "{{.Board.Name}}".

Card title: {{.Card.Title}}

Card body:
{{.Card.Body}}

First read the onboarding doc — it explains the board discipline:
curl -s "$BLERG_BOARD_URL/onboard"   (WebFetch can't reach it; blerg-board is an
in-cluster service). Your environment has BLERG_BOARD_URL, BLERG_BOARD_TOKEN,
BLERG_BOARD_BOARD, and BLERG_BOARD_CARD set; use the REST API via $BLERG_BOARD_URL.
This card is already claimed for you (linked to this session). Work like an
engineer with a lab notebook: post progress COMMENTS on the card as you go
(POST /api/cards/{id}/comments {"text": "..."}) — what you're investigating,
findings with links, decisions and why. Attach links (kind "pr" for the pull
request), and when the work is complete: post a final comment that STARTS
with the line "Review guide" and covers, in plain language a human can act
on: (1) what changed and why, (2) exact steps to verify it works, (3) risks
or things you were unsure about, (4) a "Checks:" block with one line per
fact, only for facts you actually measured:
Checks:
- tests: <passed>/<total> pass
- lint: clean | <n> issues (name the linters you ran)
- coverage: <before>% -> <after>%  (measure BEFORE your first change so
  the delta is real; skip the line if you didn't)
- version: <x.y.z>  (only if you bumped one)
- migrations: <NNN_name.sql, ...>  (only if you added any)
Then move the card to the review column. Moving to review triggers an
ADVERSARIAL REVIEW by another agent before the human looks: if it requests
changes you'll receive the findings as a message — address them, update the
PR, refresh the Review guide, and move the card back to review.
If you determine the card is wrong or obsolete, say so in a comment and
archive it.

If the card turns out to need a stronger model than you were spawned with —
or a cheaper one once the hard part is done — change your own model rather
than grinding on: POST /api/runner-sessions/self/model {"model": "...",
"reason": "..."} (MCP: blerg_session_set_model). It keeps your context and
takes effect on your next turn; the reason lands on the card's trail. A 501
means this runner can't do it — carry on with the model you have.
{{- if and .InfraDocsURL .InfraDocsNote}}

Infrastructure reference: {{.InfraDocsNote}}. For ANY infra question — how
deploys work, node layout, service conventions, where things run — read the
agent-onboarding index: curl -s "{{.InfraDocsURL}}" (in-cluster service;
WebFetch may not reach it).
{{- end}}
{{- if .Extra}}

Additional instruction from the human:
{{.Extra}}
{{- end}}
