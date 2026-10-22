package topics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func strokeBy(t *testing.T, ss []Stroke, source string) Stroke {
	t.Helper()
	for _, s := range ss {
		if s.Source == source {
			return s
		}
	}
	t.Fatalf("no %s stroke in %+v", source, ss)
	return Stroke{}
}

// Every automatic stroke comes from an event the transcript already holds, with its resolution, in
// the topic that held it; the feed is newest first.
func TestStrokesFromRecordedEvents(t *testing.T) {
	s := newScript()
	s.turn("Ship the feature", func() {
		s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "Ship it", Action: "start"})
		s.add("ask", map[string]any{"body": "Merge now?", "message_id": "m1"})
		s.add("user_message", map[string]any{"text": "yes, merge", "source": "ask_answer", "message_id": "m1"})
		s.add("ask", map[string]any{"body": "Which name?", "message_id": "m2", "no_wait": true})
		s.add("ask", map[string]any{"body": "Still there?", "message_id": "m3"})
		s.add("delegation_request", map[string]any{"request_id": "d1", "engine": "codex", "title": "Review the diff"})
		s.add("delegation_decided", map[string]any{"request_id": "d1", "status": "allowed", "choice": "once", "child_session_id": "c-secret"})
		s.add("proposal_request", map[string]any{"proposal_id": "p1", "kind": "board_file"})
		s.add("proposal_decided", map[string]any{"proposal_id": "p1", "kind": "board_file", "state": "done",
			"card": map[string]any{"id": "card-1", "board_id": "b1", "board_name": "Ops", "number": 12, "title": "Follow-up"}})
		s.add("proposal_decided", map[string]any{"proposal_id": "p2", "kind": "board_file", "state": "done", "auto": true,
			"card": map[string]any{"id": "card-2", "board_id": "b1", "number": 13}})
		s.add("review_requested", map[string]any{"request_id": "r1", "artifact_name": "plan.md", "artifact_version": 2, "ask": "Approve the plan?", "state": "pending"})
		s.add("review_decided", map[string]any{"request_id": "r1", "state": "changes_requested", "note": "split step 3"})
		s.msg("chat", "Review of plan.md (v2): 3 requests.\n\n1. tighten")
		s.add("mark", agent.MarkPayload{ID: "mk_1", Kind: "decision", By: "person", Text: "Keep the old API", PostedBy: "agent"})
		s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "Ship it", Action: "done", Outcome: "Merged", Commits: []agent.TopicCommit{{Sha: "abc1234"}}})
		s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "Ship it", Action: "done"}) // ignored: nothing open
	})
	s.add("mark", agent.MarkPayload{ID: "mk_2", Kind: "finding", By: "agent", Text: "Outside any topic", PostedBy: "agent"})
	reel := Build(s.events, nil, Options{Live: true, AskClosed: map[string]bool{"m3": true}})
	ss := reel.Strokes

	for i := 1; i < len(ss); i++ {
		if ss[i].At.After(ss[i-1].At) {
			t.Fatalf("not newest first at %d: %+v", i, ss)
		}
	}
	if ss[0].Text != "Outside any topic" || ss[0].TopicID != "" {
		t.Errorf("newest = %+v, want the mark outside every topic", ss[0])
	}
	// Plain questions never fold: each is its own line.
	byText := map[string]Stroke{}
	for _, x := range ss {
		if x.Source == "ask" {
			if x.Count != 0 {
				t.Errorf("a question folded: %+v", x)
			}
			byText[x.Text] = x
		}
	}
	if len(byText) != 3 {
		t.Fatalf("questions = %+v", byText)
	}
	if a := byText["Merge now?"]; a.State != "resolved" || a.Outcome != "yes, merge" || a.By != "person" || a.Kind != "attention" || a.Jump == nil || a.TopicID != "tp_1" {
		t.Errorf("answered ask = %+v", a)
	}
	// The agent did not wait on it: unanswered, not waiting.
	if a := byText["Which name?"]; a.State != "unanswered" || a.Outcome != "" {
		t.Errorf("no-wait question = %+v", a)
	}
	if a := byText["Still there?"]; a.State != "expired" || a.Outcome != "Closed without an answer" {
		t.Errorf("closed ask = %+v", a)
	}
	d := strokeBy(t, ss, "delegation")
	if d.Text != "Start a Codex session: Review the diff" || d.State != "resolved" || d.Outcome != "Allowed once" || d.Jump == nil || d.Jump.EventID == "" {
		t.Errorf("approval = %+v", d)
	}
	if raw, _ := json.Marshal(ss); strings.Contains(string(raw), "c-secret") {
		t.Errorf("a stroke names the child session: %s", raw)
	}
	// Two proposals in a row are not a run of three: each is its own line.
	var props []Stroke
	for _, x := range ss {
		if x.Source == "proposal" {
			props = append(props, x)
		}
	}
	if len(props) != 2 {
		t.Fatalf("proposals = %+v", props)
	}
	for _, p := range props {
		switch p.Card.Number {
		case 12:
			if p.By != "person" || p.Outcome != "Approved" || p.Card.BoardID != "b1" || p.Card.Title != "Follow-up" || p.Jump.ProposalID != "p1" || p.Jump.EventID == "" {
				t.Errorf("decided proposal = %+v", p)
			}
		case 13:
			if p.By != "permission" || p.Jump.ProposalID != "p2" {
				t.Errorf("auto-approved proposal = %+v", p)
			}
		}
	}
	r := strokeBy(t, ss, "review")
	if r.Text != "Review plan.md v2: Approve the plan?" || r.State != "declined" || r.Outcome != "Changes requested: split step 3" {
		t.Errorf("review = %+v", r)
	}
	if rr := strokeBy(t, ss, "review_round"); rr.Text != "Review of plan.md (v2): 3 requests." || rr.By != "person" {
		t.Errorf("review round = %+v", rr)
	}
	var dec Stroke
	for _, x := range ss {
		if x.Source == "mark" && x.Kind == "decision" {
			dec = x
		}
	}
	// Posted by the agent: the agent's line, recorded as the person's ruling.
	if dec.By != "agent" || dec.RecordedAs != "person" || dec.TopicID != "tp_1" || dec.Jump == nil || dec.Jump.EventID != dec.ID {
		t.Errorf("decision mark = %+v", dec)
	}
	var tops []Stroke
	for _, x := range ss {
		if x.Source == "topic" {
			tops = append(tops, x)
		}
	}
	if len(tops) != 1 || tops[0].Kind != "milestone" || tops[0].Text != "Merged" || len(tops[0].Commits) != 1 || tops[0].Commits[0].Sha != "abc1234" {
		t.Errorf("topic done with commits = %+v", tops)
	}
}

// A topic done without commits is not a milestone (its outcome is the topic's own line).
func TestStrokesSkipATopicDoneWithoutCommits(t *testing.T) {
	s := newScript()
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "x", Action: "start"})
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "x", Action: "done", Outcome: "fine"})
	if ss := Build(s.events, nil, Options{Live: true}).Strokes; len(ss) != 0 {
		t.Errorf("strokes = %+v", ss)
	}
}

// A back-dated mark sits at the moment it records, in the topic whose span holds it, and jumps to
// the person's message before that moment (none before the first).
func TestBackdatedMarksAnchorByTime(t *testing.T) {
	s := newScript()
	first := s.turn("first request", nil) // 10:02
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "x", Action: "start"})
	second := s.turn("second request", nil)
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "x", Action: "done"})
	mk := func(at string) {
		s.add("mark", agent.MarkPayload{Kind: "decision", By: "person", Text: "at " + at, At: at, PostedBy: "agent"})
	}
	mk(second.Ts.Add(30 * time.Second).Format(time.RFC3339))
	mk(first.Ts.Add(-time.Hour).Format(time.RFC3339))
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	if len(ss) != 2 {
		t.Fatalf("strokes = %+v", ss)
	}
	if !ss[0].Backdated || ss[0].Jump == nil || ss[0].Jump.EventID != second.ID || ss[0].TopicID != "tp_1" || !ss[0].At.Equal(second.Ts.Add(30*time.Second)) {
		t.Errorf("back-dated mark = %+v (want the jump to %s)", ss[0], second.ID)
	}
	if ss[1].Jump != nil || ss[1].TopicID != "" {
		t.Errorf("a mark before every message = %+v", ss[1])
	}
}

// A hostile mark is plain text on one line, with nothing invisible left in it.
func TestCleanMarkHostileText(t *testing.T) {
	now := time.Now()
	m, err := CleanMark(MarkInput{Kind: "decision", Text: "<img src=x onerror=alert(1)>\u202edone\u200b\nnext\x1b[31m"}, "agent", now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "<img src=x onerror=alert(1)>done next[31m" || m.By != "agent" || m.PostedBy != "agent" {
		t.Errorf("cleaned = %+v", m)
	}
	if _, err := CleanMark(MarkInput{Kind: "decision", Text: strings.Repeat("x", 1000)}, "agent", now); err != nil {
		t.Fatal(err)
	} else if got, _ := CleanMark(MarkInput{Kind: "decision", Text: strings.Repeat("x", 1000)}, "agent", now); len([]rune(got.Text)) != MaxMarkText {
		t.Errorf("not capped: %d", len([]rune(got.Text)))
	}
	for _, bad := range []MarkInput{
		{Kind: "attention", Text: "x"},
		{Kind: "decision", Text: " \u200b "},
		{Kind: "decision", Text: "x", By: "parent"},
		{Kind: "decision", Text: "x", Commit: "zzz"},
		{Kind: "decision", Text: "x", Card: -1},
		{Kind: "decision", Text: "x", Card: 3, BoardID: "../x"},
		{Kind: "decision", Text: "x", At: "yesterday"},
		{Kind: "decision", Text: "x", At: now.Add(time.Hour).Format(time.RFC3339)},
	} {
		if _, err := CleanMark(bad, "agent", now); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	m, err = CleanMark(MarkInput{Kind: "Milestone", By: "owner", Text: "x", Commit: "ABCDEF1", Card: 9, At: "2026-10-21T15:56Z"}, "agent", now)
	if err != nil || m.Kind != "milestone" || m.By != "person" || len(m.Commits) != 1 || m.Commits[0].Sha != "abcdef1" || m.Card != 9 || m.At != "2026-10-21T15:56:00Z" {
		t.Errorf("import line = %+v %v", m, err)
	}
	if m, _ := CleanMark(MarkInput{Kind: "decision", Text: "x"}, "owner", now); m.By != "person" {
		t.Errorf("the owner's own mark is the person's: %+v", m)
	}
}

// A back-dated mark's event id is what it says, so an import sent twice records each line once.
func TestMarkEventIDs(t *testing.T) {
	a := agent.MarkPayload{Kind: "decision", By: "person", Text: "x", At: "2026-10-21T15:56:00Z"}
	first, again, other := MarkEventID("s1", a), MarkEventID("s1", a), MarkEventID("s2", a)
	if first != again || first == other {
		t.Error("a back-dated mark's id is not stable per session")
	}
	live := agent.MarkPayload{Kind: "decision", By: "person", Text: "x"}
	if one, two := MarkEventID("s1", live), MarkEventID("s1", live); one == two {
		t.Error("two live marks share an id")
	}
}

// The sanitized fixture (testdata/strokes_fixture.jsonl, the shape of a real import) cleans line by
// line, keeps every kind and the person's and agent's decisions apart.
func TestImportFixtureCleans(t *testing.T) {
	f, err := os.Open("testdata/strokes_fixture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kinds := map[string]int{}
	bys := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var in MarkInput
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			t.Fatal(err)
		}
		m, err := CleanMark(in, "agent", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("%s: %v", sc.Text(), err)
		}
		kinds[m.Kind]++
		if m.Kind == "decision" {
			bys[m.By]++
		}
	}
	if kinds["decision"] == 0 || kinds["milestone"] == 0 || kinds["finding"] == 0 || bys["person"] == 0 || bys["agent"] == 0 {
		t.Errorf("kinds %v, decisions by %v", kinds, bys)
	}
}

// A ruling counts as the person's only when the person posted it.
func TestMarkIsThePersonsOnlyWhenThePersonPostedIt(t *testing.T) {
	s := newScript()
	s.add("mark", agent.MarkPayload{Kind: "decision", By: "person", Text: "theirs", PostedBy: "owner"})
	s.add("mark", agent.MarkPayload{Kind: "decision", By: "person", Text: "recorded", PostedBy: "agent"})
	s.add("mark", agent.MarkPayload{Kind: "decision", By: "agent", Text: "own", PostedBy: "agent"})
	got := map[string]string{}
	for _, x := range Build(s.events, nil, Options{Live: true}).Strokes {
		got[x.Text] = x.By + "/" + x.RecordedAs
	}
	want := map[string]string{"theirs": "person/", "recorded": "agent/person", "own": "agent/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Engine command approvals (owner-only asks the agent waits on) fold into one line per run; a
// plain question, a no-wait question and a back-dated mark do not, and a lone approval stays itself.
func TestCommandApprovalsCollapse(t *testing.T) {
	s := newScript()
	approval := func(id string) {
		s.add("ask", map[string]any{"body": "Run rm -rf build?", "message_id": id, "owner_only": true})
	}
	for i := 0; i < 40; i++ {
		approval(fmt.Sprintf("a%d", i))
		if i == 3 {
			// An import made while approvals come in does not cut the run.
			s.add("mark", agent.MarkPayload{Kind: "milestone", By: "agent", Text: "old", At: "2026-01-01T00:00:00Z", PostedBy: "agent"})
		}
	}
	s.add("user_message", map[string]any{"text": "allow", "source": "ask_answer", "message_id": "a0"})
	s.add("ask", map[string]any{"body": "Which name?", "message_id": "q1"})
	approval("b0")
	s.add("ask", map[string]any{"body": "Later?", "message_id": "q2", "owner_only": true, "no_wait": true})
	ss := Build(s.events, nil, Options{Live: true, AskClosed: map[string]bool{"a1": true}}).Strokes
	var lines []string
	for _, x := range ss {
		lines = append(lines, x.Source+":"+x.Text+"|"+x.State+"|"+x.Outcome)
	}
	// The run of 40 folds; the plain question ends it, so the approval after it is its own line;
	// plain questions never fold, and the one the agent did not wait on is unanswered, not waiting.
	want := []string{
		"ask:Later?|unanswered|",
		"ask:Run rm -rf build?|open|",
		"ask:Which name?|open|",
		"approvals:40 command approvals|open|38 waiting, 1 answered, 1 closed",
		"mark:old||",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for _, x := range ss {
		if x.Source == "approvals" && (x.Count != 40 || x.Jump == nil || x.Jump.Seq != x.Seq) {
			t.Errorf("collapsed line = %+v", x)
		}
	}
}

// The feed carries the newest MaxStrokes lines and counts the rest.
func TestStrokesAreCapped(t *testing.T) {
	s := newScript()
	for i := 0; i < MaxStrokes+25; i++ {
		s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: fmt.Sprintf("f%d", i), PostedBy: "agent"})
	}
	reel := Build(s.events, nil, Options{Live: true})
	if len(reel.Strokes) != MaxStrokes || reel.StrokesTruncated != 25 || reel.Strokes[0].Text != fmt.Sprintf("f%d", MaxStrokes+24) {
		t.Errorf("%d strokes, %d truncated, newest %q", len(reel.Strokes), reel.StrokesTruncated, reel.Strokes[0].Text)
	}
}

// The owner's topic: 27 board-card proposals (26 approved with their cards, 1 rejected) fold into
// one line with the cards as chips and the span; the topic's other strokes stay their own lines.
func TestRepeatedProposalsFoldIntoOneLine(t *testing.T) {
	s := newScript()
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "File the follow-ups", Action: "start"})
	s.add("mark", agent.MarkPayload{Kind: "decision", By: "agent", Text: "One card per finding", PostedBy: "agent"})
	for i := 0; i < 27; i++ {
		id := fmt.Sprintf("p%d", i)
		s.add("proposal_request", map[string]any{"proposal_id": id, "kind": "board_file"})
		if i == 13 {
			s.add("proposal_decided", map[string]any{"proposal_id": id, "kind": "board_file", "state": "rejected"})
			continue
		}
		s.add("proposal_decided", map[string]any{"proposal_id": id, "kind": "board_file", "state": "done",
			"card": map[string]any{"id": "c" + id, "board_id": "b1", "number": 44 + i}})
	}
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	if len(ss) != 2 {
		t.Fatalf("strokes = %d: %+v", len(ss), ss)
	}
	f := ss[0]
	if f.Text != "Filed 26 cards" || f.Outcome != "approved, 1 rejected" || f.Count != 27 || len(f.Items) != 27 || len(f.Cards) != 26 ||
		f.Cards[0].Number != 44 || f.State != "resolved" || f.FirstAt == nil || !f.FirstAt.Before(f.At) || f.TopicID != "tp_1" {
		t.Errorf("fold = %+v", f)
	}
	if ss[1].Source != "mark" {
		t.Errorf("the decision is its own line: %+v", ss[1])
	}
}

// A waiting member keeps the fold waiting and the jump goes to it; a fold stops at a topic's edge;
// marks are never folded.
func TestFoldWaitingTopicsAndMarks(t *testing.T) {
	s := newScript()
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "a", Action: "start"})
	s.add("delegation_request", map[string]any{"request_id": "d1", "engine": "codex", "title": "x"})
	s.add("delegation_decided", map[string]any{"request_id": "d1", "status": "allowed", "choice": "once"})
	waiting := s.add("delegation_request", map[string]any{"request_id": "d2", "engine": "codex", "title": "y"})
	s.add("delegation_request", map[string]any{"request_id": "d3", "engine": "codex", "title": "z"})
	s.add("delegation_decided", map[string]any{"request_id": "d3", "status": "denied"})
	s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: "f1", PostedBy: "agent"})
	s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: "f2", PostedBy: "agent"})
	s.add("topic", agent.TopicPayload{ID: "tp_2", Title: "b", Action: "start"})
	s.add("delegation_request", map[string]any{"request_id": "d4", "engine": "codex", "title": "w"})
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	var lines []string
	for _, x := range ss {
		lines = append(lines, x.TopicID+":"+x.Source+":"+x.Text+"|"+x.State+"|"+x.Outcome)
	}
	want := []string{
		"tp_2:delegation:Start a Codex session: w|open|",
		"tp_1:mark:f2||",
		"tp_1:mark:f1||",
		"tp_1:delegation:3 session starts|open|1 waiting, 1 allowed once, 1 denied",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines =\n%s", strings.Join(lines, "\n"))
	}
	if ss[3].Jump == nil || ss[3].Jump.EventID != waiting.ID {
		t.Errorf("the fold jumps to %+v, want the waiting start %s", ss[3].Jump, waiting.ID)
	}
}

// A waiting stroke older than the cap is kept: it is never out of sight (#121).
func TestCapKeepsWaitingStrokes(t *testing.T) {
	s := newScript()
	s.add("ask", map[string]any{"body": "Still need you", "message_id": "q0"})
	for i := 0; i < MaxStrokes+5; i++ {
		s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: fmt.Sprintf("f%d", i), PostedBy: "agent"})
	}
	reel := Build(s.events, nil, Options{Live: true})
	last := reel.Strokes[len(reel.Strokes)-1]
	if len(reel.Strokes) != MaxStrokes+1 || reel.StrokesTruncated != 5 || last.Text != "Still need you" || last.State != "open" {
		t.Errorf("%d strokes, %d truncated, last %+v", len(reel.Strokes), reel.StrokesTruncated, last)
	}
}

// A topic lists the person's messages in it, each with where it is.
func TestTopicMessages(t *testing.T) {
	s := newScript()
	first := s.turn("Start the work", func() { s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "w", Action: "start"}) })
	second := s.turn("Also do this\nwith a second line", nil)
	reel := Build(s.events, nil, Options{Live: true})
	m := reel.Topics[0].Messages
	if len(m) != 2 || m[0].EventID != first.ID || m[1].EventID != second.ID || m[1].Text != "Also do this with a second line" {
		t.Errorf("messages = %+v", m)
	}
}

// The reviewer's scenario: a session with no topics asks "Delete the prod database?" (answered NO),
// then makes five decisions, then hours later asks "Ready for lunch?". Each question stays its own
// line in its place: nothing folds them together.
func TestQuestionsAreNeverFolded(t *testing.T) {
	s := newScript()
	s.add("ask", map[string]any{"body": "Delete the prod database?", "message_id": "q1"})
	s.add("user_message", map[string]any{"text": "NO", "source": "ask_answer", "message_id": "q1"})
	for i := 0; i < 5; i++ {
		s.add("mark", agent.MarkPayload{Kind: "decision", By: "agent", Text: fmt.Sprintf("d%d", i), PostedBy: "agent"})
	}
	for i := 0; i < 3; i++ {
		s.add("ask", map[string]any{"body": fmt.Sprintf("Ready for lunch %d?", i), "message_id": fmt.Sprintf("l%d", i)})
	}
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	var lines []string
	for _, x := range ss {
		lines = append(lines, x.Text+"|"+x.State+"|"+x.Outcome)
	}
	want := []string{"Ready for lunch 2?|open|", "Ready for lunch 1?|open|", "Ready for lunch 0?|open|", "d4||", "d3||", "d2||", "d1||", "d0||", "Delete the prod database?|resolved|NO"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines =\n%s", strings.Join(lines, "\n"))
	}
}

// Only consecutive runs of three or more fold: two in a row stay two lines, and any other stroke
// between them ends a run.
func TestOnlyConsecutiveRunsOfThreeFold(t *testing.T) {
	s := newScript()
	start := func(id string) {
		s.add("delegation_request", map[string]any{"request_id": id, "engine": "codex", "title": id})
		s.add("delegation_decided", map[string]any{"request_id": id, "status": "allowed", "choice": "once"})
	}
	start("a")
	start("b")
	s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: "between", PostedBy: "agent"})
	start("c")
	start("d")
	start("e")
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	var lines []string
	for _, x := range ss {
		lines = append(lines, x.Text)
	}
	want := []string{"3 session starts", "between", "Start a Codex session: b", "Start a Codex session: a"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q", lines)
	}
}

// A fold carries at most MaxFoldItems members and cards (every waiting member too) and counts the
// rest: 3,000 approvals do not make a 3,000-item reply.
func TestFoldsAreBounded(t *testing.T) {
	s := newScript()
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("a%d", i)
		s.add("ask", map[string]any{"body": "Run it?", "message_id": id, "owner_only": true})
		if i != 10 { // the 11th is still waiting
			s.add("user_message", map[string]any{"text": "allow", "source": "ask_answer", "message_id": id})
		}
	}
	for i := 0; i < 80; i++ {
		id := fmt.Sprintf("p%d", i)
		s.add("proposal_request", map[string]any{"proposal_id": id, "kind": "board_file"})
		s.add("proposal_decided", map[string]any{"proposal_id": id, "kind": "board_file", "state": "done",
			"card": map[string]any{"id": "c" + id, "board_id": "b1", "number": 1 + i}})
	}
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	if len(ss) != 2 {
		t.Fatalf("%d lines", len(ss))
	}
	cards, approvals := ss[0], ss[1]
	if approvals.Count != 3000 || len(approvals.Items) != MaxFoldItems+1 || approvals.ItemsTruncated != 3000-MaxFoldItems-1 || approvals.State != "open" {
		t.Errorf("approvals: count %d, %d items, %d left out, %s", approvals.Count, len(approvals.Items), approvals.ItemsTruncated, approvals.State)
	}
	if last := approvals.Items[len(approvals.Items)-1]; last.State != "open" {
		t.Errorf("the waiting member is not kept: %+v", last)
	}
	if len(cards.Cards) != MaxFoldItems || cards.CardsTruncated != 30 || cards.Cards[0].Number != 31 || cards.Cards[MaxFoldItems-1].Number != 80 || cards.Text != "Filed 80 cards" {
		t.Errorf("cards: %d kept (first #%d), %d left out, %q", len(cards.Cards), cards.Cards[0].Number, cards.CardsTruncated, cards.Text)
	}
	if raw, _ := json.Marshal(ss); len(raw) > 64<<10 {
		t.Errorf("the reply is %d bytes", len(raw))
	}
}

// How a fold went: a failed proposal is "failed", superseded requests are one group, and a fold with
// no yes reads declined (or expired when every member expired).
func TestFoldOutcomes(t *testing.T) {
	line := func(states ...string) Stroke {
		s := newScript()
		for i, st := range states {
			id := fmt.Sprintf("p%d", i)
			s.add("proposal_request", map[string]any{"proposal_id": id, "kind": "board_file"})
			s.add("proposal_decided", map[string]any{"proposal_id": id, "kind": "board_file", "state": st})
		}
		ss := Build(s.events, nil, Options{Live: true}).Strokes
		if len(ss) != 1 {
			t.Fatalf("%d lines", len(ss))
		}
		return ss[0]
	}
	if l := line("done", "done", "failed"); l.Outcome != "approved, 1 failed" {
		t.Errorf("failed = %q", l.Outcome)
	}
	if l := line("rejected", "rejected", "expired"); l.State != "declined" || l.Outcome != "rejected, 1 expired" {
		t.Errorf("no yes = %s %q", l.State, l.Outcome)
	}
	if l := line("expired", "expired", "expired"); l.State != "expired" {
		t.Errorf("all expired = %s", l.State)
	}
	s := newScript()
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("r%d", i)
		s.add("review_requested", map[string]any{"request_id": id, "artifact_name": "plan.md", "artifact_version": i + 1, "ask": "OK?", "state": "pending"})
		st := map[string]any{"request_id": id, "state": "superseded", "superseded_by": i + 2}
		if i == 3 {
			st = map[string]any{"request_id": id, "state": "approved"}
		}
		s.add("review_decided", st)
	}
	if ss := Build(s.events, nil, Options{Live: true}).Strokes; len(ss) != 1 || ss[0].Outcome != "superseded, 1 approved" {
		t.Errorf("superseded = %+v", ss)
	}
}

// On a session that ended, a question and a child-session start are unanswered: nobody can answer or
// allow them any more. A proposal and a review request can still be decided (a cron session ends right
// after it queues its proposals): they stay waiting, and the cap keeps them.
func TestEndedSessionLeavesOnlyDecidableItemsWaiting(t *testing.T) {
	s := newScript()
	s.add("delegation_request", map[string]any{"request_id": "d1", "engine": "codex", "title": "x"})
	s.add("review_requested", map[string]any{"request_id": "r1", "artifact_name": "a.md", "ask": "OK?", "state": "pending"})
	s.add("ask", map[string]any{"body": "Go?", "message_id": "q1"})
	s.add("proposal_request", map[string]any{"proposal_id": "p1", "kind": "board_file"})
	got := map[string]string{}
	for _, x := range Build(s.events, nil, Options{Live: false}).Strokes {
		got[x.Source] = x.State
	}
	want := map[string]string{"delegation": "unanswered", "ask": "unanswered", "review": "open", "proposal": "open"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
}

// A cron-like session queues its write proposal and ends; the proposal waits for the person, older
// than the cap, and is still sent.
func TestCronProposalStaysWaitingAfterTheEnd(t *testing.T) {
	s := newScript()
	s.add("proposal_request", map[string]any{"proposal_id": "p1", "kind": "mcp"})
	for i := 0; i < MaxStrokes+3; i++ {
		s.add("mark", agent.MarkPayload{Kind: "finding", By: "agent", Text: fmt.Sprintf("f%d", i), PostedBy: "agent"})
	}
	reel := Build(s.events, nil, Options{Live: false})
	last := reel.Strokes[len(reel.Strokes)-1]
	if last.Source != "proposal" || last.State != "open" || reel.StrokesTruncated != 3 {
		t.Errorf("last = %+v, %d left out", last, reel.StrokesTruncated)
	}
}

// A fold whose oldest member waits keeps an id of its own: the panel pins the member, never the line.
func TestFoldLineHasItsOwnID(t *testing.T) {
	s := newScript()
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("p%d", i)
		s.add("proposal_request", map[string]any{"proposal_id": id, "kind": "board_file"})
		if i > 0 {
			s.add("proposal_decided", map[string]any{"proposal_id": id, "kind": "board_file", "state": "done",
				"card": map[string]any{"id": "c" + id, "board_id": "b1", "number": 10 + i}})
		}
	}
	ss := Build(s.events, nil, Options{Live: true}).Strokes
	if len(ss) != 1 || ss[0].State != "open" || ss[0].ID != "fold:"+ss[0].Items[3].ID || ss[0].Items[3].State != "open" {
		t.Fatalf("fold = %+v", ss)
	}
	for _, it := range ss[0].Items {
		if it.ID == ss[0].ID {
			t.Errorf("the line shares its id with a member: %s", it.ID)
		}
	}
}

// A topic lists its newest MaxTopicMessages messages and counts the rest.
func TestTopicMessagesAreBounded(t *testing.T) {
	s := newScript()
	s.add("topic", agent.TopicPayload{ID: "tp_1", Title: "w", Action: "start"})
	for i := 0; i < MaxTopicMessages+7; i++ {
		s.turn(fmt.Sprintf("message %d", i), nil)
	}
	tp := Build(s.events, nil, Options{Live: true}).Topics[0]
	if len(tp.Messages) != MaxTopicMessages || tp.MessagesTruncated != 7 || tp.Messages[len(tp.Messages)-1].Text != fmt.Sprintf("message %d", MaxTopicMessages+6) {
		t.Errorf("%d messages, %d left out", len(tp.Messages), tp.MessagesTruncated)
	}
}

// Card #155: what the person dismissed from Needs you is a stroke that says so, by the person: a
// review request (not approved, not changes requested) and a question (not answered, not expired).
func TestDismissedItemsAreStrokesByThePerson(t *testing.T) {
	s := newScript()
	s.add("review_requested", map[string]any{"request_id": "r1", "artifact_name": "plan.md", "artifact_version": 1, "ask": "Approve the plan?", "state": "pending"})
	s.add("review_decided", map[string]any{"request_id": "r1", "state": "dismissed"})
	s.add("ask", map[string]any{"body": "Which name?", "message_id": "q1", "no_wait": true})
	s.add("ask", map[string]any{"body": "Still there?", "message_id": "q2"})
	ss := Build(s.events, nil, Options{Live: true, AskClosed: map[string]bool{"q1": true, "q2": true}, AskDismissed: map[string]bool{"q1": true}}).Strokes
	got := map[string]string{}
	for _, x := range ss {
		got[x.Text] = x.State + "|" + x.By + "|" + x.Outcome
	}
	if got["Review plan.md v1: Approve the plan?"] != "expired|person|Dismissed (maybe answered in the chat)" {
		t.Errorf("review stroke = %q (all: %v)", got["Review plan.md v1: Approve the plan?"], got)
	}
	if got["Which name?"] != "expired|person|Dismissed (maybe answered in the chat)" {
		t.Errorf("dismissed question = %q", got["Which name?"])
	}
	if got["Still there?"] != "expired||Closed without an answer" {
		t.Errorf("closed (not dismissed) question = %q", got["Still there?"])
	}
}

func TestProposalWordsNameABoardProposal(t *testing.T) {
	if got := proposalWords("board_create"); got != "Create a board" {
		t.Fatalf("board proposal reads %q", got)
	}
	if proposalWords("board_file") != "File a board card" || proposalWords("mcp") != "Run a tool call that waits for approval" {
		t.Fatal("other proposal words changed")
	}
}
