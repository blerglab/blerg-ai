package topics

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

var update = flag.Bool("update", false, "rewrite testdata/reel_shape.golden.json")

// script writes a session's events one minute apart, seq from 1.
type script struct {
	seq    int64
	at     time.Time
	events []Event
	arts   []Artifact
}

func newScript() *script { return &script{at: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)} }

func (s *script) add(kind string, payload any) Event {
	s.seq++
	s.at = s.at.Add(time.Minute)
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	ev := Event{Seq: s.seq, ID: fmt.Sprintf("e%d", s.seq), Kind: kind, Payload: raw, Ts: s.at}
	s.events = append(s.events, ev)
	return ev
}

func (s *script) status(st string) {
	s.add("status_changed", agent.StatusPayload{Status: st, Reason: "turn"})
}
func (s *script) turnDone() { s.add("turn_done", agent.TurnDonePayload{StopReason: "end_turn"}) }
func (s *script) msg(source, text string) Event {
	return s.add("user_message", agent.UserMessagePayload{Text: text, Source: source})
}

// turn is one turn on the steering path: running at the write, the message at the replay, the work,
// then turn_done and idle.
func (s *script) turn(text string, work func()) Event {
	s.status("running")
	ev := s.msg("chat", text)
	if work != nil {
		work()
	}
	s.turnDone()
	s.status("idle")
	return ev
}

func (s *script) sub(id, title string) Event {
	return s.add("subagent_started", agent.SubagentPayload{ChildID: id, AgentType: "general-purpose", Model: "sonnet", Summary: title})
}
func (s *script) subEnd(p agent.SubagentPayload) Event { return s.add("subagent_done", p) }

// publish records an agent artifact event; row also keeps its session_artifacts row (false: deleted).
func (s *script) publish(id, name, view string, version int, row bool) Event {
	ev := s.add("artifact", map[string]any{"id": id, "name": name, "view": view, "origin": "agent",
		"version": version, "content_type": "text/markdown", "size": 120})
	if row {
		s.arts = append(s.arts, Artifact{ID: id, Name: name, Version: version, CreatedAt: ev.Ts})
	}
	return ev
}
func (s *script) start(id, title string) Event {
	return s.add("topic", agent.TopicPayload{ID: id, Title: title, Action: "start"})
}
func (s *script) done(id, title, outcome string) Event {
	return s.add("topic", agent.TopicPayload{ID: id, Title: title, Action: "done", Outcome: outcome})
}

func (s *script) doneWith(id, title, outcome string, shas ...string) Event {
	p := agent.TopicPayload{ID: id, Title: title, Action: "done", Outcome: outcome}
	for _, sha := range shas {
		p.Commits = append(p.Commits, agent.TopicCommit{Sha: sha})
	}
	return s.add("topic", p)
}

func (s *script) live() Reel {
	return Build(s.events, s.arts, Options{ThroughSeq: s.seq, Live: true, ShowTokens: true})
}
func (s *script) ended(at time.Time) Reel {
	return Build(s.events, s.arts, Options{ThroughSeq: s.seq, Live: false, EndedAt: &at, ShowTokens: true})
}

func titles(r Reel) []string {
	out := []string{}
	for _, t := range r.Topics {
		out = append(out, t.Title+"|"+t.Status)
	}
	return out
}

func itemTypes(t Topic) []string {
	out := []string{}
	for _, it := range t.Items {
		out = append(out, it.Type)
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %#v\n want %#v", what, got, want)
	}
}

func TestMarkersMakeTopicsNewestFirst(t *testing.T) {
	s := newScript()
	s.start("tp_a", "Build the History tab")
	s.sub("A1", "Implement task 3")
	s.done("tp_a", "Build the History tab", "Shipped on main")
	s.start("tp_b", "Fix the drawer")
	r := s.live()
	eq(t, "titles", titles(r), []string{"Fix the drawer|open", "Build the History tab|done"})
	a := r.Topics[1]
	eq(t, "a", []any{a.ID, a.Source, a.Outcome, a.StartSeq, a.EndSeq}, []any{"tp_a", "marker", "Shipped on main", int64(1), int64(3)})
	eq(t, "a items", itemTypes(a), []string{"request", "agent", "done"})
	if !a.Items[0].Marker || a.Items[0].Text != "Build the History tab" {
		t.Errorf("with no message before it, the marker is the request: %+v", a.Items[0].Request)
	}
	if a.Items[2].Outcome != "Shipped on main" || a.EndedAt == nil || !a.EndedAt.Equal(s.events[2].Ts) {
		t.Errorf("done row and ended_at: %+v %v", a.Items[2].Done, a.EndedAt)
	}
	b := r.Topics[0]
	if b.EndedAt != nil || b.EndSeq != s.seq {
		t.Errorf("the open topic runs to through_seq with no ended_at: %+v", b)
	}
	eq(t, "counts", a.Counts, Counts{Agents: 1})
}

func TestMarkerAbsorbsItsRequest(t *testing.T) {
	s := newScript()
	s.status("running")
	ask := s.msg("chat", "Build the History tab per the spec")
	s.start("tp_a", "Build the History tab")
	r := s.live()
	a := r.Topics[0]
	if a.StartSeq != ask.Seq || !a.StartedAt.Equal(ask.Ts) {
		t.Errorf("the window starts at the message: start %d at %v", a.StartSeq, a.StartedAt)
	}
	req := a.Items[0]
	eq(t, "request", []any{req.Type, req.Seq, req.EventID, req.Text, req.Marker},
		[]any{"request", ask.Seq, ask.ID, "Build the History tab per the spec", false})
}

func TestMarkerDoesNotReachBackPastAnotherTopicEvent(t *testing.T) {
	s := newScript()
	s.msg("chat", "Do A then B")
	s.start("tp_a", "A")
	s.done("tp_a", "A", "")
	b := s.start("tp_b", "B")
	r := s.live()
	got := r.Topics[0]
	if got.StartSeq != b.Seq || !got.Items[0].Marker {
		t.Errorf("B must not take the message A already took: %+v", got.Items[0])
	}
}

func TestStartOverStartEndsTheFirst(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	s.sub("A1", "x")
	b := s.start("tp_b", "B")
	r := s.live()
	eq(t, "titles", titles(r), []string{"B|open", "A|ended"})
	a := r.Topics[1]
	if a.EndSeq != b.Seq-1 || a.EndedAt == nil || !a.EndedAt.Equal(b.Ts) {
		t.Errorf("A ends where B starts: end %d at %v", a.EndSeq, a.EndedAt)
	}
}

func TestStrayDoneIsIgnored(t *testing.T) {
	s := newScript()
	s.done("tp_x", "X", "nothing")
	s.turn("Hello", nil)
	r := s.live()
	if len(r.Topics) != 0 || r.Unmarked != nil {
		t.Errorf("a done with nothing open makes nothing: %+v", r)
	}
}

// A marker takes as its request only a message of the person's (§3.2's checks); before anything
// else it is its own request.
func TestOnlyAPersonMessageIsARequest(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(s *script)
	}{
		{"harness text recorded as chat", func(s *script) { s.status("idle"); s.msg("chat", "<task-notification><task-id>t</task-id>") }},
		{"harness line from the system", func(s *script) { s.msg("system", "<task-notification><task-id>t</task-id>") }},
		{"old restart note", func(s *script) { s.msg("chat", "[system] Session resumed after a restart.") }},
		{"ask answer", func(s *script) { s.msg("ask_answer", "yes") }},
		{"model change", func(s *script) { s.msg("chat", "/model claude-haiku-5") }},
		{"review", func(s *script) { s.msg("chat", "Review of plan.md (v2): 3 requests.") }},
		{"review without version", func(s *script) { s.msg("chat", "Review of notes.md: 1 request.") }},
		{"mark-up", func(s *script) { s.msg("chat", "Marked up s.png (see the attached image).") }},
		// The first message drawn as the person's is a /model change, so the runner message is not first.
		{"later runner message", func(s *script) { s.msg("chat", "/model claude-haiku-5"); s.msg("runner", "Ship it") }},
		{"a message in an earlier turn", func(s *script) { s.turn("Just a question?", nil) }},
	} {
		s := newScript()
		c.run(s)
		s.start("tp_a", "A")
		r := s.live()
		if req := r.Topics[0].Items[0].Request; !req.Marker || req.Text != "A" {
			t.Errorf("%s: want the marker as the request, got %+v", c.name, req)
		}
	}
}

func TestAnsweredAskThenLateMarkerMakesOneTopic(t *testing.T) {
	// The person asks; the agent asks back (waiting is a turn end), gets the answer, and only then
	// marks the topic and starts work. The marker cannot reach back past the turn end, and the work
	// is the marker's: one topic, nothing unmarked.
	s := newScript()
	s.status("running")
	s.msg("chat", "Build x")
	s.status("waiting")
	s.msg("ask_answer", "yes")
	s.status("running")
	s.start("tp_x", "Build x per the answer")
	s.sub("A1", "x")
	r := s.live()
	eq(t, "titles", titles(r), []string{"Build x per the answer|open"})
	eq(t, "source", r.Topics[0].Source, "marker")
	if r.Unmarked != nil {
		t.Errorf("nothing is unmarked: %+v", r.Unmarked)
	}
}

func TestFirstRunnerMessageIsARequest(t *testing.T) {
	s := newScript()
	s.msg("runner", "Ship the release notes")
	s.start("tp_a", "Release notes")
	if req := s.live().Topics[0].Items[0].Request; req.Marker || req.Text != "Ship the release notes" {
		t.Errorf("the start prompt from the runner contract is the marker's request: %+v", req)
	}
}

func TestSessionEnd(t *testing.T) {
	s := newScript()
	s.turn("Build x", func() {
		s.start("tp_x", "Build x")
		s.sub("A1", "never finished")
	})
	end := s.at.Add(time.Hour)
	r := s.ended(end)
	h := r.Topics[0]
	if h.Status != "ended" || h.EndedAt == nil || !h.EndedAt.Equal(end) {
		t.Errorf("session end ends the last topic at the session's end: %+v", h)
	}
	if st := h.Items[1].Status; st != "stopped" {
		t.Errorf("a subagent with no end in an ended session = %q, want stopped", st)
	}
}

func TestDocumentInTwoTopicsAndADeletedVersion(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	v1 := s.publish("art1", "plan.md", "markdown", 1, false) // its row was deleted
	s.publish("art2", "plan.md", "markdown", 2, true)
	s.start("tp_b", "B")
	v3 := s.publish("art3", "plan.md", "markdown", 3, true)
	r := s.live()
	b, a := r.Topics[0], r.Topics[1]
	da, db := a.Items[1], b.Items[1]
	if da.Type != "document" || db.Type != "document" {
		t.Fatalf("each topic lists the document once: %v / %v", itemTypes(a), itemTypes(b))
	}
	if da.Seq != v1.Seq+1 || db.Seq != v3.Seq {
		t.Errorf("a document sits at the newest version its topic published: %d, %d", da.Seq, db.Seq)
	}
	eq(t, "A's versions", da.Versions, []Version{
		{Version: 1, ArtifactID: "art1", CreatedAt: v1.Ts, InTopic: true, Deleted: true},
		{Version: 2, ArtifactID: "art2", CreatedAt: s.arts[0].CreatedAt, InTopic: true},
		{Version: 3, ArtifactID: "art3", CreatedAt: v3.Ts},
	})
	if da.LatestVersion != 3 || db.LatestVersion != 3 {
		t.Errorf("latest_version is the name's newest: %d %d", da.LatestVersion, db.LatestVersion)
	}
	if !db.Versions[2].InTopic || db.Versions[0].InTopic {
		t.Errorf("B published v3 only: %+v", db.Versions)
	}
	eq(t, "counts", []Counts{a.Counts, b.Counts}, []Counts{{Documents: 1}, {Documents: 1}})
}

// A version whose row survived but whose bytes did not is listed, flagged missing (not deleted),
// and says so in the JSON; a present version carries no flag at all.
func TestDocumentMissingVersion(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	v1 := s.publish("art1", "plan.md", "markdown", 1, true)
	v2 := s.publish("art2", "plan.md", "markdown", 2, true)
	s.arts[0].Missing = true
	d := s.live().Topics[0].Items[1]
	eq(t, "versions", d.Versions, []Version{
		{Version: 1, ArtifactID: "art1", CreatedAt: v1.Ts, InTopic: true, Missing: true},
		{Version: 2, ArtifactID: "art2", CreatedAt: v2.Ts, InTopic: true},
	})
	raw, err := json.Marshal(d.Versions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"missing":true`) != 1 || strings.Contains(string(raw), `"missing":false`) {
		t.Errorf("missing appears once, only when true: %s", raw)
	}
}

func TestMockDocuments(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	s.publish("h1", "mock.html", "html", 1, true)
	s.publish("i1", "shot.png", "image", 1, true)
	s.publish("p1", "paper.pdf", "pdf", 1, true)
	got := map[string]bool{}
	for _, it := range s.live().Topics[0].Items {
		if it.Document != nil {
			got[it.Name] = it.Mock
		}
	}
	eq(t, "mock", got, map[string]bool{"mock.html": true, "shot.png": true, "paper.pdf": false})
}

func TestSubagentStatuses(t *testing.T) {
	for _, c := range []struct {
		name string
		ends []agent.SubagentPayload
		live bool
		want string
	}{
		{"done", []agent.SubagentPayload{{Summary: "All green"}}, true, "done"},
		{"is_error", []agent.SubagentPayload{{Summary: "it broke", IsError: true}}, true, "error"},
		{"error summary (older daemon)", []agent.SubagentPayload{{Summary: "error: it broke"}}, true, "error"},
		{"cut off", []agent.SubagentPayload{{Summary: "stopped: the daemon restarted"}}, true, "stopped"},
		{"killed by a person", []agent.SubagentPayload{{Summary: "killed: the task was stopped"}}, true, "stopped"},
		{"killed, no body", []agent.SubagentPayload{{Summary: "killed"}}, true, "stopped"},
		{"stopped, no body", []agent.SubagentPayload{{Summary: "stopped"}}, true, "stopped"},
		{"a summary that only starts with the word", []agent.SubagentPayload{{Summary: "stoppedness is fine"}}, true, "done"},
		{"background, live", []agent.SubagentPayload{{Summary: "running in the background", Background: true}}, true, "background"},
		{"background, then its end", []agent.SubagentPayload{{Background: true}, {Summary: "The build is green."}}, true, "done"},
		{"no end, live", nil, true, "running"},
		{"no end, ended", nil, false, "stopped"},
		{"background, ended", []agent.SubagentPayload{{Background: true}}, false, "stopped"},
	} {
		s := newScript()
		s.start("tp_a", "A")
		s.sub("A1", "Implement task 3")
		for _, e := range c.ends {
			e.ChildID = "A1"
			s.subEnd(e)
		}
		r := s.live()
		if !c.live {
			r = s.ended(s.at)
		}
		if got := r.Topics[0].Items[1].Status; got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAgentRowTakesTheNewestFigures(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	s.sub("A1", "Implement task 3")
	s.add("subagent_update", agent.SubagentPayload{ChildID: "A1", Model: "claude-haiku-5", Usage: &agent.Usage{InputTokens: 10}, ToolUses: 2})
	s.subEnd(agent.SubagentPayload{ChildID: "A1", Model: "claude-sonnet-5", Summary: "ok", Usage: &agent.Usage{InputTokens: 40, OutputTokens: 5}, ToolUses: 7, DurationMs: 412000})
	a := s.live().Topics[0].Items[1].Agent
	eq(t, "agent", *a, Agent{CallID: "A1", Title: "Implement task 3", AgentType: "general-purpose", Model: "claude-sonnet-5",
		Usage: &agent.Usage{InputTokens: 40, OutputTokens: 5}, ToolUses: 7, DurationMs: 412000, Status: "done"})
}

func TestTokensHidden(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	s.sub("A1", "x")
	s.subEnd(agent.SubagentPayload{ChildID: "A1", Summary: "ok", Usage: &agent.Usage{OutputTokens: 9}})
	r := Build(s.events, s.arts, Options{ThroughSeq: s.seq, Live: true, ShowTokens: false})
	if !r.TokensHidden || r.Topics[0].Items[1].Usage != nil {
		t.Errorf("hidden tokens: %v %+v", r.TokensHidden, r.Topics[0].Items[1].Usage)
	}
}

// A done's commits ride on its row, each with its web page when the session's repository is known
// (null otherwise), and count on the topic; a done without them has neither.
func TestDoneCarriesItsCommits(t *testing.T) {
	s := newScript()
	s.turn("Fix x", func() {
		s.start("tp_1", "Fix x")
		s.doneWith("tp_1", "Fix x", "Fixed", "1a2b3c4", "9f8e7d6")
	})
	s.turn("Fix y", func() {
		s.start("tp_2", "Fix y")
		s.done("tp_2", "Fix y", "Fixed")
	})
	r := s.live()
	x, y := r.Topics[1], r.Topics[0]
	d := x.Items[len(x.Items)-1].Done
	if d == nil || len(d.Commits) != 2 || d.Commits[0].Sha != "1a2b3c4" || d.Commits[1].Sha != "9f8e7d6" || d.Commits[0].URL != nil {
		t.Fatalf("done = %+v", d)
	}
	eq(t, "counts", x.Counts, Counts{Commits: 2})
	if raw, _ := json.Marshal(x.Items[len(x.Items)-1]); !strings.Contains(string(raw), `"commits":[{"sha":"1a2b3c4","url":null},`) {
		t.Errorf("an unknown repository's commit has url null: %s", raw)
	}
	if raw, _ := json.Marshal(y); strings.Contains(string(raw), "commits") {
		t.Errorf("a done without commits has no commits key: %s", raw)
	}
	link := Build(s.events, s.arts, Options{ThroughSeq: s.seq, Live: true, CommitURL: func(sha string) string {
		if sha == "9f8e7d6" {
			return ""
		}
		return "https://git.example.test/o/r/commit/" + sha
	}})
	d = link.Topics[1].Items[len(link.Topics[1].Items)-1].Done
	if d.Commits[0].URL == nil || *d.Commits[0].URL != "https://git.example.test/o/r/commit/1a2b3c4" || d.Commits[1].URL != nil {
		t.Errorf("links = %+v", d.Commits)
	}
}

// The wire shape: one fixture with every item type, compared to a reviewed golden file
// (go test ./internal/topics -run TestReelJSONShape -update rewrites it).
func TestReelJSONShape(t *testing.T) {
	s := newScript()
	s.turn("Build the History tab per the spec", func() {
		s.start("tp_5f1c2a9e", "Build the History tab")
		s.publish("art1", "plan.md", "markdown", 1, true)
		s.sub("toolu_1", "Implement task 3")
		s.subEnd(agent.SubagentPayload{ChildID: "toolu_1", AgentType: "general-purpose", Model: "claude-sonnet-5", Summary: "ok",
			Usage: &agent.Usage{InputTokens: 1, OutputTokens: 2}, ToolUses: 37, DurationMs: 412000})
		s.add("proposal_decided", map[string]any{"proposal_id": "p1", "kind": "board_file", "state": "done", "auto": true,
			"card": map[string]any{"id": "c1", "board_id": "b1", "board_name": "Fixture board", "number": 12, "title": "Badge is stale", "type": "bug", "priority": "high", "column": "Backlog"}})
		s.add("session_renamed", map[string]any{"from": "Worker", "to": "Build the History tab", "by": "parent"})
		s.doneWith("tp_5f1c2a9e", "Build the History tab", "Shipped on main", "1a2b3c4", "9f8e7d6")
	})
	s.turn("Thanks, and check the badge", func() {
		s.publish("art2", "notes.md", "markdown", 1, true)
		s.sub("toolu_2", "Check the badge")
	})
	reel := Build(s.events, s.arts, Options{ThroughSeq: s.seq, Live: true, ShowTokens: true,
		CommitURL: func(sha string) string { return "https://github.com/example-org/example-repo/commit/" + sha }})
	got, err := json.MarshalIndent(reel, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/reel_shape.golden.json"
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(append(got, '\n')) != string(want) {
		t.Errorf("reel JSON drifted from %s:\n%s", path, got)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	if _, old := m["untitled"]; old {
		t.Errorf("untitled is gone (R7): %v", m["untitled"])
	}
	if u, _ := m["unmarked"].(map[string]any); u == nil || u["agents"] != float64(1) || len(u["documents"].([]any)) != 1 {
		t.Errorf("unmarked = %v", m["unmarked"])
	}
	counts := m["topics"].([]any)[0].(map[string]any)["counts"].(map[string]any)
	if counts["commits"] != float64(2) || counts["cards"] != float64(1) || len(counts) != 4 {
		t.Errorf("counts = %v, want documents, agents, commits and cards", counts)
	}
}

func TestMarkerDoesNotReachBackPastATurnEnd(t *testing.T) {
	s := newScript()
	s.turn("Build X", func() { s.sub("A1", "x") })
	var mk Event
	s.turn("Review of plan.md (v2): 3 requests.", func() { mk = s.start("tp_r", "Address review") })
	r := s.live()
	eq(t, "titles", titles(r), []string{"Address review|open"})
	m := r.Topics[0]
	if r.Unmarked == nil || r.Unmarked.Agents != 1 {
		t.Errorf("the earlier turn's work is unmarked: %+v", r.Unmarked)
	}
	if m.StartSeq != mk.Seq || !m.Items[0].Marker || m.Items[0].Text != "Address review" {
		t.Errorf("a marker after a turn end starts at itself: start %d, request %+v", m.StartSeq, m.Items[0].Request)
	}
}

func TestEventsOutOfOrder(t *testing.T) {
	s := newScript()
	s.turn("Build the History tab per the spec", func() {
		s.start("tp_a", "Build the History tab")
		s.publish("art1", "plan.md", "markdown", 1, true)
		s.sub("A1", "Implement task 3")
		s.done("tp_a", "Build the History tab", "Shipped")
	})
	s.turn("Fix the drawer", func() { s.sub("A2", "y") })
	want, _ := json.Marshal(s.live())
	rev := make([]Event, len(s.events))
	for i, ev := range s.events {
		rev[len(rev)-1-i] = ev
	}
	got, _ := json.Marshal(Build(rev, s.arts, Options{Live: true, ShowTokens: true}))
	if string(got) != string(want) {
		t.Errorf("events out of seq order build a different reel:\n got  %s\n want %s", got, want)
	}
	if rev[0].Seq != s.seq {
		t.Errorf("Build reordered the caller's slice")
	}
}

func TestSubagentWithoutIDIsNoRow(t *testing.T) {
	s := newScript()
	s.turn("Build x", func() { s.add("subagent_started", agent.SubagentPayload{Summary: "no id"}) })
	if r := s.live(); len(r.Topics) != 0 || r.Unmarked != nil {
		t.Errorf("a subagent_started with no child_id is no agent row: %v %+v", titles(r), r.Unmarked)
	}
}

func TestDocumentVersionsCapped(t *testing.T) {
	nums := func(vs []Version) (all, in []int) {
		for _, v := range vs {
			all = append(all, v.Version)
			if v.InTopic {
				in = append(in, v.Version)
			}
		}
		return all, in
	}
	span := func(from, to int) []int {
		out := []int{}
		for v := from; v <= to; v++ {
			out = append(out, v)
		}
		return out
	}
	s := newScript()
	s.start("tp_a", "A")
	for v := 1; v <= 3; v++ {
		s.publish(fmt.Sprintf("a%d", v), "plan.md", "markdown", v, true)
	}
	s.start("tp_b", "B")
	for v := 4; v <= 30; v++ {
		s.publish(fmt.Sprintf("a%d", v), "plan.md", "markdown", v, true)
	}
	r := s.live()
	da, db := r.Topics[1].Items[1].Document, r.Topics[0].Items[1].Document
	all, in := nums(da.Versions)
	eq(t, "A: its own versions and the newest 20", all, append(span(1, 3), span(11, 30)...))
	eq(t, "A in topic", in, span(1, 3))
	all, in = nums(db.Versions)
	eq(t, "B: every version it published, all newer than the cut", all, span(4, 30))
	eq(t, "B in topic", in, span(4, 30))
	eq(t, "totals", []int{da.VersionsTotal, db.VersionsTotal, da.LatestVersion}, []int{30, 30, 30})

	// One file republished in each of 300 topics: each row lists 20 versions and its own, not all 300.
	s = newScript()
	for i := 1; i <= 300; i++ {
		s.turn(fmt.Sprintf("Revise the plan, pass %d", i), func() {
			s.start(fmt.Sprintf("tp_%d", i), "Revise the plan")
			s.publish(fmt.Sprintf("p%d", i), "plan.md", "markdown", i, true)
		})
	}
	r = s.live()
	listed := 0
	for _, tp := range r.Topics {
		for _, it := range tp.Items {
			if it.Document != nil {
				listed += len(it.Versions)
				if it.VersionsTotal != 300 {
					t.Fatalf("versions_total = %d, want 300", it.VersionsTotal)
				}
			}
		}
	}
	if len(r.Topics) != 300 || listed > 300*21 {
		t.Errorf("%d topics list %d versions; want 300 topics and at most %d", len(r.Topics), listed, 300*21)
	}
}

// R7: a session without markers has no topics; its work is one unmarked summary (the agents, every
// file at its newest version, the span), however its messages and turns fall.
func TestNoMarkersMakeOneUnmarkedSummary(t *testing.T) {
	s := newScript()
	first := s.turn("Fix the drawer on phones", func() { s.sub("A1", "Survey") })
	s.turn("ok, restarted daemon", func() {
		s.publish("p1", "plan.md", "markdown", 1, true)
		s.sub("A2", "Review")
	})
	s.turn("Thanks", nil)
	var last Event
	s.turn("and the badge", func() {
		s.publish("p2", "plan.md", "markdown", 2, true)
		last = s.sub("A3", "Fix the badge")
	})
	r := s.live()
	if len(r.Topics) != 0 {
		t.Fatalf("no marker, no topic: %v", titles(r))
	}
	u := r.Unmarked
	if u == nil {
		t.Fatal("the work is summarised as unmarked")
	}
	eq(t, "span", []any{u.Agents, u.FirstSeq, u.LastSeq, u.StartedAt, u.EndedAt},
		[]any{3, first.Seq + 1, last.Seq, s.events[first.Seq].Ts, last.Ts})
	eq(t, "subagents", agentTitles(u.Subagents), []string{"Survey", "Review", "Fix the badge"})
	if len(u.Documents) != 1 || u.Documents[0].Name != "plan.md" || u.Documents[0].LatestVersion != 2 || u.Documents[0].VersionsTotal != 2 {
		t.Fatalf("one row per file at its newest version: %+v", u.Documents)
	}
	if v := u.Documents[0].Versions; len(v) != 2 || !v[0].InTopic || !v[1].InTopic {
		t.Errorf("both versions were published outside a marker: %+v", v)
	}
}

// Work outside the marker windows (before the first start, after a done) is unmarked; work inside
// stays the topic's.
func TestUnmarkedAroundAMarker(t *testing.T) {
	s := newScript()
	before := s.sub("A0", "before anything")
	s.start("tp_a", "A")
	s.sub("A1", "inside")
	s.publish("d1", "notes.md", "markdown", 1, true)
	s.done("tp_a", "A", "ok")
	s.publish("d2", "notes.md", "markdown", 2, true)
	after := s.sub("A2", "after the done")
	r := s.live()
	eq(t, "titles", titles(r), []string{"A|done"})
	eq(t, "topic", itemTypes(r.Topics[0]), []string{"request", "agent", "document", "done"})
	u := r.Unmarked
	if u == nil {
		t.Fatal("leftovers are unmarked")
	}
	eq(t, "unmarked", []any{u.Agents, u.FirstSeq, u.LastSeq, agentTitles(u.Subagents)},
		[]any{2, before.Seq, after.Seq, []string{"before anything", "after the done"}})
	if len(u.Documents) != 1 {
		t.Fatalf("documents = %+v", u.Documents)
	}
	if v := u.Documents[0].Versions; len(v) != 2 || v[0].InTopic || !v[1].InTopic {
		t.Errorf("only v2 was published outside the marker: %+v", v)
	}
	s2 := newScript()
	s2.start("tp_a", "A")
	s2.sub("A1", "x")
	if r2 := s2.live(); r2.Unmarked != nil {
		t.Errorf("nothing outside a window, no summary: %+v", r2.Unmarked)
	}
}

func agentTitles(items []Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

// An unmarked file's row sits at its earliest unmarked version, so the summary's span starts at the
// first thing done outside a window, not at a later republish.
func TestUnmarkedSpanStartsAtAFilesFirstVersion(t *testing.T) {
	s := newScript()
	v1 := s.publish("p1", "plan.md", "markdown", 1, true)
	s.sub("A1", "x")
	v2 := s.publish("p2", "plan.md", "markdown", 2, true)
	u := s.live().Unmarked
	if u == nil || len(u.Documents) != 1 {
		t.Fatalf("unmarked = %+v", u)
	}
	eq(t, "span", []any{u.FirstSeq, u.StartedAt, u.LastSeq, u.Documents[0].Seq, u.Documents[0].LatestVersion},
		[]any{v1.Seq, v1.Ts, v2.Seq, v1.Seq, 2})
	if !u.EndedAt.Equal(v2.Ts) || !u.Documents[0].Versions[1].InTopic {
		t.Errorf("the republish ends the span and is in the unmarked list: %v %+v", u.EndedAt, u.Documents[0].Versions)
	}
}

// A resumed run is a row of its own, "(resumed)", in the topic its start falls in; the original's
// row stays done where it was. A resumed run of a background original reads background until it ends.
func TestAResumedSubagentIsARowOfItsOwn(t *testing.T) {
	s := newScript()
	s.start("tp_a", "A")
	s.sub("A1", "Implement task 3")
	s.subEnd(agent.SubagentPayload{ChildID: "A1", Summary: "ok"})
	s.start("tp_b", "B")
	s.add("subagent_started", agent.SubagentPayload{ChildID: "S1", AgentType: "general-purpose", Summary: "Implement task 3", Resumed: true, ResumedFrom: "A1"})
	s.sub("A2", "Watch")
	s.subEnd(agent.SubagentPayload{ChildID: "A2", Summary: "running in the background", Background: true})
	s.subEnd(agent.SubagentPayload{ChildID: "A2", Summary: "green"})
	s.add("subagent_started", agent.SubagentPayload{ChildID: "S2", AgentType: "Explore", Summary: "Watch", Background: true, Resumed: true, ResumedFrom: "A2"})
	r := s.live()
	rows := func(tp Topic) []string {
		out := []string{}
		for _, it := range tp.Items {
			if it.Agent != nil {
				out = append(out, it.CallID+"|"+it.Title+"|"+it.Status)
			}
		}
		return out
	}
	eq(t, "B (newest first)", rows(r.Topics[0]), []string{"S1|Implement task 3 (resumed)|running", "A2|Watch|done", "S2|Watch (resumed)|background"})
	eq(t, "A", rows(r.Topics[1]), []string{"A1|Implement task 3|done"})
	if !r.Topics[0].Items[1].Resumed || r.Topics[0].Counts.Agents != 3 {
		t.Errorf("resumed row = %+v, counts %+v", r.Topics[0].Items[1].Agent, r.Topics[0].Counts)
	}
	s.subEnd(agent.SubagentPayload{ChildID: "S1", Summary: "fixed", Resumed: true, ResumedFrom: "A1"})
	eq(t, "after its end", rows(s.live().Topics[0])[0], "S1|Implement task 3 (resumed)|done")
}

// A card an approved proposal created is a chip of the topic that was open, else of the unmarked
// summary; other outcomes (rejected, failed, unknown) and malformed cards are no chip.
func TestReelCardsFromApprovedProposals(t *testing.T) {
	card := func(state string, number int) map[string]any {
		return map[string]any{"proposal_id": "p-" + state, "kind": "board_file", "state": state,
			"card": map[string]any{"id": "c", "board_id": "b", "board_name": "Board", "number": number, "title": "T"}}
	}
	s := newScript()
	s.add("proposal_decided", card("done", 3)) // before any topic: unmarked
	s.turn("Do the thing", func() {
		s.start("tp_1", "Do the thing")
		s.add("proposal_decided", card("done", 4))
		s.add("proposal_decided", card("rejected", 5))
		s.add("proposal_decided", card("unknown", 6))
		s.add("proposal_decided", card("done", 0))
		s.done("tp_1", "Do the thing", "Done")
	})
	r := s.live()
	if len(r.Topics) != 1 || r.Topics[0].Counts.Cards != 1 {
		t.Fatalf("topic cards = %+v", r.Topics)
	}
	var got *Card
	for _, it := range r.Topics[0].Items {
		if it.Type == "card" {
			got = it.Card
		}
	}
	if got == nil || got.Number != 4 || got.BoardName != "Board" || got.ProposalID != "p-done" {
		t.Fatalf("topic card = %+v", got)
	}
	if r.Unmarked == nil || len(r.Unmarked.Cards) != 1 || r.Unmarked.Cards[0].Card.Number != 3 || len(r.Unmarked.Subagents) != 0 {
		t.Fatalf("unmarked = %+v", r.Unmarked)
	}
}

// A rename is a row of the topic that was open, else of the unmarked work; an event that is not a
// rename (no new title, an unknown author) is dropped, and long titles are cut.
func TestRenamesAreRowsOfTheirWindow(t *testing.T) {
	s := newScript()
	s.add("session_renamed", map[string]any{"from": "", "to": "First", "by": "person"})
	s.add("session_renamed", map[string]any{"from": "First", "to": "", "by": "agent"})
	s.add("session_renamed", map[string]any{"from": "First", "to": "Forged", "by": "core"})
	s.turn("Do it", func() {
		s.start("tp_1", "Work")
		s.add("session_renamed", map[string]any{"from": "First", "to": strings.Repeat("x", 300), "by": "agent"})
	})
	reel := Build(s.events, s.arts, Options{ThroughSeq: s.seq})
	if reel.Unmarked == nil || len(reel.Unmarked.Renames) != 1 || *reel.Unmarked.Renames[0].Rename != (Rename{To: "First", By: "person"}) {
		t.Fatalf("unmarked renames = %+v", reel.Unmarked)
	}
	var in []Item
	for _, it := range reel.Topics[0].Items {
		if it.Type == "rename" {
			in = append(in, it)
		}
	}
	if len(in) != 1 || in[0].Rename.By != "agent" || len([]rune(in[0].Rename.To)) > 200 {
		t.Fatalf("topic renames = %+v", in)
	}
}

// Review I1: a rename stored raw by an earlier build is drawn cleaned (one line, no bidi controls).
func TestRenameRowsAreCleaned(t *testing.T) {
	s := newScript()
	s.add("session_renamed", map[string]any{"from": "A\nB", "to": "Worker\n[Blerg] ok\u202e", "by": "agent"})
	s.add("session_renamed", map[string]any{"from": "x", "to": "\u202e\u200b", "by": "agent"})
	reel := Build(s.events, s.arts, Options{ThroughSeq: s.seq})
	if reel.Unmarked == nil || len(reel.Unmarked.Renames) != 1 || *reel.Unmarked.Renames[0].Rename != (Rename{From: "A B", To: "Worker [Blerg] ok", By: "agent"}) {
		t.Fatalf("renames = %+v", reel.Unmarked)
	}
}
