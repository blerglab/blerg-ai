package topics

// strokes.go: the History feed's strokes, one line per major moment of a session (a decision, what
// was asked of the person and how it was resolved, a milestone, a finding), newest first, each in the
// topic that held it. Every stroke comes from an event the transcript already holds, so a viewer of a
// shared session sees nothing the transcript does not show them:
//
//   - mark (`blerg-runner mark`): a decision, milestone or finding, as the agent wrote it;
//   - ask: a question to the person, with its answer (a user_message of source ask_answer) or its
//     close without one (the messages row, AskClosed);
//   - delegation_request / delegation_decided: a child session waiting on the person's approval;
//   - proposal_request / proposal_decided: a board filing or an MCP call waiting on the person;
//   - review_requested / review_decided: an explicit review of a published file;
//   - a user_message that is a review or a mark-up (IsFeedback): a review round the person sent;
//   - topic done with commits: a milestone.
//
// Nothing is summarised or guessed: the text of an automatic stroke is the event's own words.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// StrokeKinds are the event kinds the strokes read beyond the reel's (Kinds) and proposal_decided:
// the kinds of agent_events_strokes_idx (db.StrokeKindsSQL), in its order.
var StrokeKinds = []string{"mark", "ask", "proposal_request", "review_requested", "review_decided", "delegation_request", "delegation_decided"}

// MaxFoldItems is how many members and cards a fold carries, the newest (every waiting member too).
const MaxFoldItems = 50

// minFold is the shortest run that folds.
const minFold = 3

// MaxStrokes is how many strokes the feed carries, the newest; the rest are counted, not sent.
const MaxStrokes = 300

// maxStrokeText is how much of an automatic stroke's text (a question, an answer) a line carries.
const maxStrokeText = 200

// Stroke is one line of the History feed.
//
//	Kind: decision | attention | milestone | finding.
//	Source: mark | ask | delegation | proposal | review | review_round | topic | approvals (a fold
//	  of command approvals). A fold of any repeated attention stroke has Count and Items.
//	By: whose it is: "person" or "agent" for a mark; for an attention stroke, who resolved it
//	  ("person", "parent": the session that started this one, "permission": a session permission
//	  approved it), "" while it is open.
//	State: an attention stroke's: open | resolved | declined | expired; "" for the rest.
//	Outcome: how an attention stroke was resolved, in words ("Allowed once", the answer).
//	Backdated: a mark that records an earlier moment (an import): At is that moment, and its jump is
//	  the person's message before it (or none).
type Stroke struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"`
	Source    string      `json:"source"`
	By        string      `json:"by,omitempty"`
	Text      string      `json:"text"`
	State     string      `json:"state,omitempty"`
	Outcome   string      `json:"outcome,omitempty"`
	At        time.Time   `json:"at"`
	Seq       int64       `json:"seq"`
	TopicID   string      `json:"topic_id,omitempty"`
	Jump      *Jump       `json:"jump"`
	Commits   []Commit    `json:"commits,omitempty"`
	Card      *StrokeCard `json:"card,omitempty"`
	Backdated bool        `json:"backdated,omitempty"`
	// RecordedAs is "person" on the agent's line recording a ruling of the person's (`--by person`).
	RecordedAs string `json:"recorded_as,omitempty"`
	// Count is how many strokes a folded line stands for; Items are they, newest first; FirstAt is
	// the oldest one's time; Cards are their cards, oldest first (foldRuns).
	Count   int          `json:"count,omitempty"`
	Items   []Stroke     `json:"items,omitempty"`
	FirstAt *time.Time   `json:"first_at,omitempty"`
	Cards   []StrokeCard `json:"cards,omitempty"`
	// ItemsTruncated and CardsTruncated count a fold's older members and cards left out (MaxFoldItems).
	ItemsTruncated int `json:"items_truncated,omitempty"`
	CardsTruncated int `json:"cards_truncated,omitempty"`
	// approval: an engine's command approval (an owner-only ask the agent waits on); noWait: a
	// question the agent did not wait on.
	approval bool
	noWait   bool
}

// Jump is where "show in conversation" goes: the transcript row of EventID (its data-event-id), else
// the proposal card of ProposalID.
type Jump struct {
	EventID    string `json:"event_id,omitempty"`
	ProposalID string `json:"proposal_id,omitempty"`
	// Seq is the row's seq: the chat can reach it only when it holds that part of the transcript.
	Seq int64 `json:"seq"`
}

// StrokeCard is a board card a stroke names: its number, and its board when the stroke knows it (a
// filed card's, or the board the mark named). Never its body.
type StrokeCard struct {
	BoardID   string `json:"board_id,omitempty"`
	BoardName string `json:"board_name,omitempty"`
	Number    int    `json:"number"`
	Title     string `json:"title,omitempty"`
}

type askState struct {
	stroke   *Stroke
	answered bool
}

// strokes builds the feed from events (seq order), in the topic windows wins.
func strokes(events []Event, wins []*Topic, opt Options) []Stroke {
	var out []*Stroke
	asks := map[string]*askState{}      // message id
	delegations := map[string]*Stroke{} // request id
	proposals := map[string]*Stroke{}   // proposal id
	reviews := map[string]*Stroke{}     // request id
	var persons []Event                 // the person's messages, the anchors of back-dated marks
	seenShaped := false
	add := func(s *Stroke) *Stroke {
		out = append(out, s)
		return s
	}
	for _, ev := range events {
		switch ev.Kind {
		case "mark":
			var p agent.MarkPayload
			if json.Unmarshal(ev.Payload, &p) != nil || !MarkKinds[p.Kind] {
				continue
			}
			text := CleanText(p.Text, MaxMarkText)
			if text == "" {
				continue
			}
			// Ownership and posting identity are separate: --by person records the person's ruling.
			by, recorded := "agent", ""
			if p.By == "person" {
				by = "person"
				if p.PostedBy != "owner" {
					recorded = "person"
				}
			}
			s := &Stroke{ID: ev.ID, Kind: p.Kind, Source: "mark", By: by, RecordedAs: recorded, Text: text, At: ev.Ts, Seq: ev.Seq,
				Commits: doneCommits(p.Commits, opt.CommitURL), Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}}
			if p.Card > 0 && p.Card <= MaxCard {
				board := ""
				if boardIDRe.MatchString(p.BoardID) {
					board = p.BoardID
				}
				s.Card = &StrokeCard{BoardID: board, Number: p.Card}
			}
			if p.At != "" {
				if at, err := ParseMarkTime(p.At); err == nil {
					s.At, s.Backdated, s.Jump = at, true, nil
				}
			}
			add(s)
		case "ask":
			var p struct {
				Body      string `json:"body"`
				MessageID string `json:"message_id"`
				OwnerOnly bool   `json:"owner_only"`
				NoWait    bool   `json:"no_wait"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.MessageID == "" || asks[p.MessageID] != nil {
				continue
			}
			s := add(&Stroke{ID: ev.ID, Kind: "attention", Source: "ask", Text: CleanText(p.Body, maxStrokeText), State: "open", At: ev.Ts, Seq: ev.Seq,
				Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}, approval: p.OwnerOnly && !p.NoWait, noWait: p.NoWait})
			asks[p.MessageID] = &askState{stroke: s}
		case "user_message":
			var p agent.UserMessagePayload
			var extra struct {
				AnsweredBy string `json:"answered_by"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			_ = json.Unmarshal(ev.Payload, &extra)
			if p.Source == "ask_answer" {
				if a := asks[p.MessageID]; a != nil && !a.answered {
					a.answered = true
					a.stroke.State, a.stroke.By = "resolved", "person"
					if extra.AnsweredBy == "session" {
						a.stroke.By = "parent"
					}
					a.stroke.Outcome = CleanText(p.Text, maxStrokeText)
				}
				continue
			}
			if !personShape(p) {
				continue
			}
			first := !seenShaped
			seenShaped = true
			if IsFeedback(p.Text) && (p.Source == "chat" || p.Source == "human" || p.Source == "review") {
				head, _, _ := strings.Cut(strings.TrimSpace(p.Text), "\n")
				add(&Stroke{ID: ev.ID, Kind: "attention", Source: "review_round", By: "person", Text: CleanText(head, maxStrokeText),
					State: "resolved", At: ev.Ts, Seq: ev.Seq, Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}})
				continue
			}
			if IsPersonMessage(p, first) {
				persons = append(persons, ev)
			}
		case "delegation_request":
			var p struct {
				RequestID string `json:"request_id"`
				Engine    string `json:"engine"`
				Title     string `json:"title"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.RequestID == "" || delegations[p.RequestID] != nil {
				continue
			}
			what := CleanText(p.Title, maxStrokeText)
			if what == "" {
				what = "a session"
			}
			engine := CleanText(p.Engine, 32)
			text := "Start a session: " + what
			if engine != "" {
				text = "Start a " + engineWord(engine) + " session: " + what
			}
			delegations[p.RequestID] = add(&Stroke{ID: ev.ID, Kind: "attention", Source: "delegation", Text: Cut(text, maxStrokeText), State: "open",
				At: ev.Ts, Seq: ev.Seq, Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}})
		case "delegation_decided":
			var p struct {
				RequestID string `json:"request_id"`
				Status    string `json:"status"`
				Choice    string `json:"choice"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			s := delegations[p.RequestID]
			if s == nil {
				continue
			}
			switch p.Status {
			case "allowed":
				s.State, s.By = "resolved", "person"
				switch p.Choice {
				case "session":
					s.Outcome = "Allowed for this session"
				case "always":
					s.Outcome = "Always allowed"
				default:
					s.Outcome = "Allowed once"
				}
			case "denied":
				s.State, s.By, s.Outcome = "declined", "person", "Denied"
			case "expired":
				s.State, s.By, s.Outcome = "expired", "", "Expired, nobody answered"
			}
		case "proposal_request":
			var p struct {
				ProposalID string `json:"proposal_id"`
				Kind       string `json:"kind"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.ProposalID == "" || proposals[p.ProposalID] != nil {
				continue
			}
			proposals[p.ProposalID] = add(&Stroke{ID: ev.ID, Kind: "attention", Source: "proposal", Text: proposalWords(p.Kind), State: "open",
				At: ev.Ts, Seq: ev.Seq, Jump: &Jump{EventID: ev.ID, ProposalID: p.ProposalID, Seq: ev.Seq}})
		case "proposal_decided":
			var p struct {
				ProposalID string `json:"proposal_id"`
				Kind       string `json:"kind"`
				State      string `json:"state"`
				Auto       bool   `json:"auto"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.ProposalID == "" {
				continue
			}
			s := proposals[p.ProposalID]
			if s == nil {
				// Approved by a session permission without asking: the decision is its only event.
				s = add(&Stroke{ID: ev.ID, Kind: "attention", Source: "proposal", Text: proposalWords(p.Kind), At: ev.Ts, Seq: ev.Seq,
					Jump: &Jump{ProposalID: p.ProposalID, Seq: ev.Seq}})
				proposals[p.ProposalID] = s
			}
			who := "person"
			if p.Auto {
				who = "permission"
			}
			switch p.State {
			case "done":
				s.State, s.By, s.Outcome = "resolved", who, "Approved"
			case "executing":
				s.State, s.By, s.Outcome = "resolved", who, "Approved"
			case "rejected":
				s.State, s.By, s.Outcome = "declined", who, "Rejected"
			case "expired":
				s.State, s.By, s.Outcome = "expired", "", "Expired, nobody answered"
			case "failed":
				s.State, s.By, s.Outcome = "resolved", who, "Approved, but it failed"
			}
			if c := cardOf(ev); c != nil {
				s.Card = &StrokeCard{BoardID: c.BoardID, BoardName: c.BoardName, Number: c.Number, Title: c.Title}
			}
		case "review_requested":
			var p struct {
				RequestID       string `json:"request_id"`
				ArtifactName    string `json:"artifact_name"`
				ArtifactVersion int    `json:"artifact_version"`
				Ask             string `json:"ask"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.RequestID == "" || reviews[p.RequestID] != nil {
				continue
			}
			text := "Review " + CleanText(p.ArtifactName, 120)
			if p.ArtifactVersion > 0 {
				text += " v" + itoa(p.ArtifactVersion)
			}
			if ask := CleanText(p.Ask, maxStrokeText); ask != "" {
				text += ": " + ask
			}
			reviews[p.RequestID] = add(&Stroke{ID: ev.ID, Kind: "attention", Source: "review", Text: Cut(text, maxStrokeText), State: "open",
				At: ev.Ts, Seq: ev.Seq, Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}})
		case "review_decided":
			var p struct {
				RequestID    string `json:"request_id"`
				State        string `json:"state"`
				Note         string `json:"note"`
				SupersededBy int    `json:"superseded_by"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			s := reviews[p.RequestID]
			if s == nil {
				continue
			}
			note := CleanText(p.Note, maxStrokeText)
			with := func(word string) string {
				if note != "" {
					return word + ": " + note
				}
				return word
			}
			switch p.State {
			case "approved":
				s.State, s.By, s.Outcome = "resolved", "person", with("Approved")
			case "changes_requested":
				s.State, s.By, s.Outcome = "declined", "person", with("Changes requested")
			case "dismissed":
				s.State, s.By, s.Outcome = "expired", "person", "Dismissed (maybe answered in the chat)"
			case "withdrawn":
				s.State, s.By, s.Outcome = "expired", "agent", "Withdrawn"
			case "superseded":
				s.State, s.By, s.Outcome = "expired", "agent", "Superseded"
				if p.SupersededBy > 0 {
					s.Outcome = "Superseded by v" + itoa(p.SupersededBy)
				}
			}
		case "topic":
			var p agent.TopicPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.Action != "done" {
				continue
			}
			commits := doneCommits(p.Commits, opt.CommitURL)
			if len(commits) == 0 {
				continue
			}
			text := CleanText(p.Outcome, MaxOutcome)
			if text == "" {
				text = CleanText(p.Title, MaxTitle)
			}
			add(&Stroke{ID: ev.ID, Kind: "milestone", Source: "topic", Text: text, At: ev.Ts, Seq: ev.Seq, Commits: commits, Jump: &Jump{EventID: ev.ID, Seq: ev.Seq}})
		}
	}
	for id, a := range asks {
		switch {
		case a.answered:
		case opt.AskDismissed[id]:
			a.stroke.State, a.stroke.By, a.stroke.Outcome = "expired", "person", "Dismissed (maybe answered in the chat)"
		case opt.AskClosed[id]:
			a.stroke.State, a.stroke.Outcome = "expired", "Closed without an answer"
		case a.stroke.noWait:
			// A question the agent did not wait on: nobody is held up by it, so it is not waiting.
			a.stroke.State = "unanswered"
		}
	}
	if !opt.Live {
		// The session ended: a question can no longer reach the agent, and a child session can no
		// longer be started for it (the server refuses the decision), so neither waits on anyone. A
		// proposal (until its own deadline) and a review request can still be decided: they stay
		// waiting until their own decision or expiry arrives.
		for _, s := range out {
			if s.State == "open" && (s.Source == "ask" || s.Source == "delegation") {
				s.State = "unanswered"
			}
		}
	}
	for _, s := range out {
		if s.Backdated {
			s.Jump = anchor(persons, s.At)
			s.TopicID = topicAtTime(wins, s.At)
		} else {
			s.TopicID = topicAtSeq(wins, s.Seq)
		}
	}
	res := foldRuns(out)
	// Newest first: by the moment each records, then by seq.
	sort.SliceStable(res, func(i, j int) bool {
		if !res[i].At.Equal(res[j].At) {
			return res[i].At.After(res[j].At)
		}
		return res[i].Seq > res[j].Seq
	})
	return res
}

// foldKey is what makes two strokes "the same stroke" for folding: attention strokes of one source
// (a board filing apart from a tool call). Plain questions never fold: each carries its own text and
// answer. Marks and topic milestones never fold either: each was written on purpose. "" for a stroke
// that does not fold.
func foldKey(s *Stroke) string {
	if s.Backdated || s.Kind != "attention" {
		return ""
	}
	switch s.Source {
	case "ask":
		if s.approval {
			return "approvals"
		}
		return ""
	case "proposal":
		return "proposal:" + s.Text
	case "delegation", "review", "review_round":
		return s.Source
	}
	return ""
}

// foldRuns folds each run of minFold or more consecutive strokes of one kind (foldKey) into one line,
// in seq order: any other stroke of the live transcript ends a run, and so does a topic's edge. A
// back-dated mark records another moment: it never cuts a run. A shorter run stays its strokes.
func foldRuns(out []*Stroke) []Stroke {
	res := make([]Stroke, 0, len(out))
	var run []*Stroke
	key := ""
	flush := func() {
		if len(run) >= minFold {
			res = append(res, foldLine(key, run))
		} else {
			for _, m := range run {
				res = append(res, *m)
			}
		}
		run, key = nil, ""
	}
	for _, s := range out {
		if s.Backdated {
			res = append(res, *s)
			continue
		}
		k := foldKey(s)
		if len(run) > 0 && (k != key || s.TopicID != run[0].TopicID) {
			flush()
		}
		if k == "" {
			res = append(res, *s)
			continue
		}
		key = k
		run = append(run, s)
	}
	flush()
	return res
}

// foldLine is the one line of a fold (members in seq order). It carries the newest MaxFoldItems
// members and every waiting one (the rest counted), and the newest MaxFoldItems cards.
func foldLine(key string, members []*Stroke) Stroke {
	newest := members[len(members)-1]
	n := len(members)
	// The line's own id (its oldest member's, prefixed): stable as the fold grows, and never one of
	// its members' ids, which the panel pins on their own.
	line := Stroke{ID: "fold:" + members[0].ID, Kind: "attention", Source: newest.Source, At: newest.At, Seq: newest.Seq,
		TopicID: newest.TopicID, Jump: newest.Jump, Count: n}
	first := members[0].At
	line.FirstAt = &first
	waiting, resolved, expired := 0, 0, 0
	tally := map[string]int{}
	var order []string
	for _, m := range members {
		switch m.State {
		case "open":
			waiting++
			line.Jump = m.Jump
			continue
		case "resolved":
			resolved++
		case "expired", "unanswered":
			expired++
		}
		w := foldWord(m)
		if tally[w] == 0 {
			order = append(order, w)
		}
		tally[w]++
	}
	for i := n - 1; i >= 0; i-- {
		m := members[i]
		if len(line.Items) < MaxFoldItems || m.State == "open" {
			line.Items = append(line.Items, *m)
		}
	}
	line.ItemsTruncated = n - len(line.Items)
	filed := 0
	for _, m := range members {
		if m.Card != nil {
			filed++
		}
	}
	for i := n - 1; i >= 0 && len(line.Cards) < MaxFoldItems; i-- {
		if members[i].Card != nil {
			line.Cards = append([]StrokeCard{*members[i].Card}, line.Cards...)
		}
	}
	line.CardsTruncated = filed - len(line.Cards)
	switch {
	case key == "approvals":
		line.Source, line.Text = "approvals", itoa(n)+" command approvals"
	case key == "proposal:File a board card":
		line.Text = itoa(n) + " card proposals"
		if filed > 0 {
			line.Text = "Filed " + itoa(filed) + " cards"
		}
	case strings.HasPrefix(key, "proposal:"):
		line.Text = itoa(n) + " tool call proposals"
	case key == "delegation":
		line.Text = itoa(n) + " session starts"
	case key == "review":
		line.Text = itoa(n) + " review requests"
	case key == "review_round":
		line.Text, line.By = itoa(n)+" review rounds", "person"
	}
	// The line's state: waiting when any member is; else resolved when any was; else every member was
	// closed without a yes: expired when all of them expired, declined otherwise.
	switch {
	case waiting > 0:
		line.State = "open"
	case resolved > 0:
		line.State = "resolved"
	case expired == n:
		line.State = "expired"
	default:
		line.State = "declined"
	}
	// The most common way they went, bare; the others with their counts: "approved, 1 rejected".
	sort.SliceStable(order, func(i, j int) bool { return tally[order[i]] > tally[order[j]] })
	var parts []string
	if waiting > 0 {
		parts = append(parts, itoa(waiting)+" waiting")
	}
	for i, w := range order {
		if i == 0 && waiting == 0 && w != "" {
			parts = append(parts, w)
		} else if w != "" {
			parts = append(parts, itoa(tally[w])+" "+w)
		}
	}
	line.Outcome = strings.Join(parts, ", ")
	if waiting == 0 && len(order) == 1 {
		line.By = newest.By
		for _, m := range members {
			if m.By != line.By {
				line.By = "" // resolved by several: the items say who
			}
		}
	}
	return line
}

// foldWord is how a folded member went, in a word or two: "approved", "rejected", "failed",
// "allowed once", "changes requested", "superseded"; an approval's "answered" or "closed".
func foldWord(m *Stroke) string {
	if m.State == "unanswered" {
		return "unanswered"
	}
	if m.Source == "ask" || m.Source == "review_round" {
		switch m.State {
		case "resolved":
			if m.Source == "review_round" {
				return "sent"
			}
			return "answered"
		case "declined", "expired":
			return "closed"
		}
		return m.State
	}
	lower := strings.ToLower(m.Outcome)
	switch {
	case strings.Contains(lower, "failed"):
		return "failed"
	case strings.HasPrefix(lower, "superseded"):
		return "superseded"
	}
	w := lower
	if i := strings.IndexAny(w, ":,"); i >= 0 {
		w = w[:i]
	}
	w = strings.TrimSpace(w)
	if w == "" {
		return m.State
	}
	return w
}

// CapStrokes keeps the newest limit strokes of a feed (newest first), and every waiting one beyond
// them (a waiting stroke is never out of sight), and says how many it left out.
func CapStrokes(ss []Stroke, limit int) ([]Stroke, int) {
	if len(ss) <= limit {
		return ss, 0
	}
	kept := append([]Stroke(nil), ss[:limit]...)
	for _, s := range ss[limit:] {
		if s.State == "open" {
			kept = append(kept, s)
		}
	}
	return kept, len(ss) - len(kept)
}

// anchor is the jump of a back-dated stroke: the person's newest message at or before at, or nil.
func anchor(persons []Event, at time.Time) *Jump {
	i := sort.Search(len(persons), func(i int) bool { return persons[i].Ts.After(at) }) - 1
	if i < 0 {
		return nil
	}
	return &Jump{EventID: persons[i].ID, Seq: persons[i].Seq}
}

// topicAtSeq is the id of the window that holds seq, "" when none does.
func topicAtSeq(wins []*Topic, seq int64) string {
	for _, w := range wins {
		if seq >= w.StartSeq && seq <= w.EndSeq {
			return w.ID
		}
	}
	return ""
}

// topicAtTime is the id of the window whose span holds at (the last one, when spans touch).
func topicAtTime(wins []*Topic, at time.Time) string {
	id := ""
	for _, w := range wins {
		if at.Before(w.StartedAt) {
			continue
		}
		if w.EndedAt == nil || !at.After(*w.EndedAt) {
			id = w.ID
		}
	}
	return id
}

func proposalWords(kind string) string {
	switch kind {
	case "mcp":
		return "Run a tool call that waits for approval"
	case "board_create":
		return "Create a board"
	}
	return "File a board card"
}

// engineWord is an engine's name as a person reads it.
func engineWord(engine string) string {
	switch engine {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "hermes":
		return "Hermes"
	case "openclaw":
		return "OpenClaw"
	}
	return engine
}

func itoa(n int) string { return strconv.Itoa(n) }
