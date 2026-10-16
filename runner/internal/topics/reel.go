package topics

// reel.go: Build — a session's topics from its events (the kinds in Kinds, seq order) and its
// agent-published files. A topic is a marker window (the agent's `blerg-runner topic`); every
// document and subagent joins the window that holds its event's seq, and what no window holds is
// folded into one unmarked summary (R7: no topic is guessed from the person's messages).

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// Event is one transcript event of a kind in Kinds. ID is its client_event_id.
type Event struct {
	Seq     int64
	ID      string
	Kind    string
	Payload json.RawMessage
	Ts      time.Time
}

// Artifact is one agent-published session_artifacts row (a row that is gone is a deleted version).
type Artifact struct {
	ID        string
	Name      string
	Version   int
	CreatedAt time.Time
	Missing   bool // the row is here, its bytes are not (see Version.Missing)
}

// Options: ThroughSeq is the session's newest seq (0: the last event's); Live says the session can
// still run something; EndedAt is its end, once it ended; ShowTokens is the Insights rule's answer;
// CommitURL is a commit's web page in the session's repository ("" or nil: not known).
type Options struct {
	ThroughSeq int64
	Live       bool
	EndedAt    *time.Time
	ShowTokens bool
	CommitURL  func(sha string) string
}

// Reel is GET /api/sessions/{id}/topics' body, less the session fields the handler adds.
type Reel struct {
	Topics       []Topic   `json:"topics"`
	Unmarked     *Unmarked `json:"unmarked"`
	TokensHidden bool      `json:"tokens_hidden"`
}

// Unmarked is the work no marker window holds, as one summary: how many subagents ran (Subagents
// lists them), each file published, at its newest version (a version's in_topic: published outside
// every window), and the span from the first such row to the last. nil when everything is inside a
// window.
type Unmarked struct {
	Agents    int       `json:"agents"`
	Documents []Item    `json:"documents"`
	Subagents []Item    `json:"subagents"`
	FirstSeq  int64     `json:"first_seq"`
	LastSeq   int64     `json:"last_seq"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Topic is one piece of work the agent marked (Source is always "marker"). Status: open | done | ended.
type Topic struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Outcome   string     `json:"outcome,omitempty"`
	StartSeq  int64      `json:"start_seq"`
	EndSeq    int64      `json:"end_seq"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Counts    Counts     `json:"counts"`
	Items     []Item     `json:"items"`
}

// Counts omits zeros, so a count a daemon cannot report (F2), or commits nobody named, never reads 0.
type Counts struct {
	Documents int `json:"documents,omitempty"`
	Agents    int `json:"agents,omitempty"`
	Commits   int `json:"commits,omitempty"`
}

// Item is one timeline row; exactly one of the embedded parts is set, by Type
// (request | document | agent | done), and its fields sit beside type/seq/event_id/ts.
type Item struct {
	Type    string    `json:"type"`
	Seq     int64     `json:"seq"`
	EventID string    `json:"event_id"`
	Ts      time.Time `json:"ts"`
	*Request
	*Document
	*Agent
	*Done
}

// Request is the opening message, or the marker itself (Marker) when no message comes before it.
type Request struct {
	Text   string `json:"text"`
	Marker bool   `json:"marker,omitempty"`
}

// Done is how a marked topic ended: its outcome and the commits the agent named (`--commit`).
type Done struct {
	Outcome string   `json:"outcome"`
	Commits []Commit `json:"commits,omitempty"`
}

// Commit is one commit a done names; URL is its web page, null when the session's repository is
// not known.
type Commit struct {
	Sha string  `json:"sha"`
	URL *string `json:"url"`
}

// doneCommits reads a done's commits: the server checked them, but an event is input, so a sha that
// is not one is dropped, as is a repeat, and at most MaxCommits are kept.
func doneCommits(in []agent.TopicCommit, url func(string) string) []Commit {
	var out []Commit
	seen := map[string]bool{}
	for _, c := range in {
		sha := strings.ToLower(c.Sha)
		if !shaRe.MatchString(sha) || seen[sha] || len(out) == MaxCommits {
			continue
		}
		seen[sha] = true
		cm := Commit{Sha: sha}
		if url != nil {
			if u := url(sha); u != "" {
				cm.URL = &u
			}
		}
		out = append(out, cm)
	}
	return out
}

// Document is one file name a topic published, at the newest version it published, with the name's
// newest maxVersions versions and every version this topic published (VersionsTotal counts them
// all). ContentType and Size are the shown version's (for a thumbnail).
type Document struct {
	Name          string    `json:"name"`
	View          string    `json:"view"`
	Mock          bool      `json:"mock"`
	ContentType   string    `json:"content_type"`
	Size          int64     `json:"size"`
	LatestVersion int       `json:"latest_version"`
	VersionsTotal int       `json:"versions_total"`
	Versions      []Version `json:"versions"`
}

type Version struct {
	Version    int       `json:"version"`
	ArtifactID string    `json:"artifact_id"`
	CreatedAt  time.Time `json:"created_at"`
	InTopic    bool      `json:"in_topic"`
	Deleted    bool      `json:"deleted"`
	// Missing: the version is still listed but its bytes are no longer stored (a data directory
	// lost on a restart); it cannot be opened.
	Missing bool `json:"missing,omitempty"`
}

// Agent is one subagent. Status: running | background | done | error | stopped. A resumed run (a
// finished subagent the main agent resumed with SendMessage) is a row of its own, its CallID the
// SendMessage call's, its Title the original's with " (resumed)".
type Agent struct {
	CallID     string       `json:"call_id"`
	Title      string       `json:"title"`
	AgentType  string       `json:"agent_type"`
	Model      string       `json:"model"`
	Usage      *agent.Usage `json:"usage"`
	ToolUses   int          `json:"tool_uses"`
	DurationMs int64        `json:"duration_ms"`
	Status     string       `json:"status"`
	Resumed    bool         `json:"resumed,omitempty"`
}

// Build makes the reel. events: the session's events of the kinds in Kinds (sorted by seq here).
func Build(events []Event, arts []Artifact, opt Options) Reel {
	events = append([]Event(nil), events...) // seq order, without touching the caller's slice
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	if opt.ThroughSeq == 0 && len(events) > 0 {
		opt.ThroughSeq = events[len(events)-1].Seq
	}
	persons, marks, turnEnds := scan(events)
	wins := markerWindows(persons, marks, turnEnds, opt.CommitURL)
	closeWindows(wins, events, opt)
	rest := &Topic{}
	collect(wins, rest, events, arts, opt)
	reel := Reel{Topics: make([]Topic, 0, len(wins)), TokensHidden: !opt.ShowTokens}
	for i := len(wins) - 1; i >= 0; i-- {
		finish(wins[i])
		reel.Topics = append(reel.Topics, *wins[i])
	}
	reel.Unmarked = unmarked(rest)
	return reel
}

// unmarked folds the rows no window holds into the summary, nil when there are none.
func unmarked(rest *Topic) *Unmarked {
	if len(rest.Items) == 0 {
		return nil
	}
	finish(rest)
	first, last := rest.Items[0], rest.Items[len(rest.Items)-1]
	u := &Unmarked{Agents: rest.Counts.Agents, Documents: []Item{}, Subagents: []Item{},
		FirstSeq: first.Seq, LastSeq: last.Seq, StartedAt: first.Ts, EndedAt: last.Ts}
	if rest.EndSeq > u.LastSeq { // a file republished after the last row
		u.LastSeq, u.EndedAt = rest.EndSeq, *rest.EndedAt
	}
	for _, it := range rest.Items {
		if it.Type == "document" {
			u.Documents = append(u.Documents, it)
		} else {
			u.Subagents = append(u.Subagents, it)
		}
	}
	return u
}

// person is a user_message that passes IsPersonMessage: a marker may take it as its request.
type person struct {
	ev   Event
	text string
}

type mark struct {
	ev Event
	p  agent.TopicPayload
}

// scan reads the person messages, the topic markers and the seqs of the turn ends (turn_done, or a
// change to idle or waiting).
func scan(events []Event) (persons []person, marks []mark, turnEnds []int64) {
	seenShaped := false
	for _, ev := range events {
		switch ev.Kind {
		case "turn_done":
			turnEnds = append(turnEnds, ev.Seq)
		case "status_changed":
			var p agent.StatusPayload
			if json.Unmarshal(ev.Payload, &p) == nil && (p.Status == "idle" || p.Status == "waiting") {
				turnEnds = append(turnEnds, ev.Seq)
			}
		case "user_message":
			var p agent.UserMessagePayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if personShape(p) {
				first := !seenShaped
				seenShaped = true
				if IsPersonMessage(p, first) {
					persons = append(persons, person{ev: ev, text: p.Text})
				}
			}
		case "topic":
			var p agent.TopicPayload
			if json.Unmarshal(ev.Payload, &p) == nil && p.ID != "" {
				marks = append(marks, mark{ev: ev, p: p})
			}
		}
	}
	return persons, marks, turnEnds
}

// artifactPayload is the part of an artifact event the reel reads (the server's artifactInfo).
type artifactPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	View        string `json:"view"`
	Origin      string `json:"origin"`
	Version     int    `json:"version"`
}

// agentArtifact reads an artifact event the agent published that is a document (not a review file).
func agentArtifact(ev Event) (artifactPayload, bool) {
	var p artifactPayload
	if json.Unmarshal(ev.Payload, &p) != nil || p.ID == "" || p.Name == "" {
		return p, false
	}
	if p.Origin != "" && p.Origin != "agent" {
		return p, false
	}
	return p, !strings.HasSuffix(p.Name, ".review.json")
}

func requestItem(p person) Item {
	return Item{Type: "request", Seq: p.ev.Seq, EventID: p.ev.ID, Ts: p.ev.Ts, Request: &Request{Text: RequestText(p.text)}}
}

func endAt(t *Topic, seq int64, at time.Time, status string) {
	t.EndSeq, t.Status = seq, status
	a := at
	t.EndedAt = &a
}

// lastPersonBefore is the newest person message before seq, or nil.
func lastPersonBefore(persons []person, seq int64) *person {
	i := sort.Search(len(persons), func(i int) bool { return persons[i].ev.Seq >= seq }) - 1
	if i < 0 {
		return nil
	}
	return &persons[i]
}

// markerWindows: a start opens a window (and ends the open one); a done closes the open one; a done
// with nothing open is ignored. EndSeq 0 means the window runs on. A start takes the person message
// before it as its request only within the same turn and with no other topic event in between.
func markerWindows(persons []person, marks []mark, turnEnds []int64, commitURL func(string) string) []*Topic {
	var wins []*Topic
	var open *Topic
	var lastMark int64
	for _, m := range marks {
		switch m.p.Action {
		case "start":
			w := &Topic{ID: m.p.ID, Source: "marker", Title: m.p.Title, Status: "open", StartSeq: m.ev.Seq, StartedAt: m.ev.Ts}
			req := Item{Type: "request", Seq: m.ev.Seq, EventID: m.ev.ID, Ts: m.ev.Ts, Request: &Request{Text: m.p.Title, Marker: true}}
			if pp := lastPersonBefore(persons, m.ev.Seq); pp != nil && pp.ev.Seq > lastMark && !between(turnEnds, pp.ev.Seq, m.ev.Seq) {
				w.StartSeq, w.StartedAt = pp.ev.Seq, pp.ev.Ts
				req = requestItem(*pp)
			}
			w.Items = append(w.Items, req)
			if open != nil {
				endAt(open, w.StartSeq-1, w.StartedAt, "ended")
			}
			wins = append(wins, w)
			open = w
		case "done":
			if open != nil {
				endAt(open, m.ev.Seq, m.ev.Ts, "done")
				open.Outcome = m.p.Outcome
				open.Items = append(open.Items, Item{Type: "done", Seq: m.ev.Seq, EventID: m.ev.ID, Ts: m.ev.Ts, Done: &Done{Outcome: m.p.Outcome, Commits: doneCommits(m.p.Commits, commitURL)}})
				open = nil
			}
		}
		lastMark = m.ev.Seq
	}
	return wins
}

// between reports a seq of the sorted seqs strictly between from and to.
func between(seqs []int64, from, to int64) bool {
	i := sort.Search(len(seqs), func(i int) bool { return seqs[i] > from })
	return i < len(seqs) && seqs[i] < to
}

// closeWindows ends every open window at the next one's start; the last runs to ThroughSeq, and is
// ended by the session's end.
func closeWindows(wins []*Topic, events []Event, opt Options) {
	for i, w := range wins {
		if w.Status != "open" {
			continue
		}
		if i+1 < len(wins) {
			next := wins[i+1]
			endAt(w, next.StartSeq-1, next.StartedAt, "ended")
			continue
		}
		w.EndSeq = opt.ThroughSeq
		if !opt.Live {
			var at time.Time
			if opt.EndedAt != nil {
				at = *opt.EndedAt
			} else if len(events) > 0 {
				at = events[len(events)-1].Ts
			}
			endAt(w, opt.ThroughSeq, at, "ended")
		}
	}
}

type agentAcc struct {
	topic    *Topic
	item     Item
	launched bool                   // a background launch's subagent_done was seen, or a resumed run of a background one started
	done     *agent.SubagentPayload // the plain subagent_done (its end)
}

func (a *agentAcc) status(live bool) string {
	if d := a.done; d != nil {
		switch {
		case d.IsError || strings.HasPrefix(d.Summary, "error:"):
			return "error"
		case stoppedSummary(d.Summary): // cut off, or a person stopped it
			return "stopped"
		}
		return "done"
	}
	switch {
	case !live:
		return "stopped"
	case a.launched:
		return "background"
	}
	return "running"
}

// stoppedSummary: the daemon's "stopped" / "killed" summary, bare or with ": <body>".
func stoppedSummary(s string) bool {
	for _, w := range []string{"stopped", "killed"} {
		if s == w || strings.HasPrefix(s, w+": ") {
			return true
		}
	}
	return false
}

// maxVersions: a document row lists its name's newest maxVersions versions, plus every version its
// topic published; VersionsTotal is the name's full count.
const maxVersions = 20

type docVersion struct{ v Version }

type docKey struct {
	t    *Topic
	name string
}

// collect puts each document and subagent into the window that holds its event (else rest).
func collect(wins []*Topic, rest *Topic, events []Event, arts []Artifact, opt Options) {
	at := func(seq int64) *Topic {
		i := sort.Search(len(wins), func(i int) bool { return wins[i].StartSeq > seq }) - 1
		if i >= 0 && seq <= wins[i].EndSeq {
			return wins[i]
		}
		return rest
	}
	rows := map[string]bool{}
	versions := map[string]map[string]*docVersion{} // name -> artifact id -> version
	add := func(name, id string) *docVersion {
		if versions[name] == nil {
			versions[name] = map[string]*docVersion{}
		}
		dv := versions[name][id]
		if dv == nil {
			dv = &docVersion{v: Version{ArtifactID: id}}
			versions[name][id] = dv
		}
		return dv
	}
	for _, a := range arts {
		rows[a.ID] = true
		dv := add(a.Name, a.ID)
		dv.v.Version, dv.v.CreatedAt, dv.v.Missing = a.Version, a.CreatedAt, a.Missing
	}
	shown := map[docKey]Event{}
	shownP := map[docKey]artifactPayload{}
	inTopic := map[docKey]map[string]bool{}
	var docOrder []docKey
	agents := map[string]*agentAcc{}
	var agentOrder []*agentAcc
	for _, ev := range events {
		switch ev.Kind {
		case "artifact":
			p, ok := agentArtifact(ev)
			if !ok {
				continue
			}
			dv := add(p.Name, p.ID)
			if !rows[p.ID] {
				dv.v.Version, dv.v.CreatedAt, dv.v.Deleted = p.Version, ev.Ts, true
			}
			k := docKey{at(ev.Seq), p.Name}
			_, seen := shown[k]
			if !seen {
				docOrder = append(docOrder, k)
				inTopic[k] = map[string]bool{}
			}
			if k.t == rest {
				// An unmarked row sits at its first version (the summary's span starts there); a later
				// version still ends the span.
				rest.EndSeq, rest.EndedAt = ev.Seq, &ev.Ts
				if seen {
					shownP[k] = p
					inTopic[k][p.ID] = true
					continue
				}
			}
			shown[k], shownP[k] = ev, p
			inTopic[k][p.ID] = true
		case "subagent_started":
			var p agent.SubagentPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.ChildID == "" {
				continue
			}
			title := p.Summary
			if p.Resumed {
				title = strings.TrimSpace(title + " (resumed)")
			}
			acc := &agentAcc{topic: at(ev.Seq), launched: p.Resumed && p.Background, item: Item{Type: "agent", Seq: ev.Seq, EventID: ev.ID, Ts: ev.Ts,
				Agent: &Agent{CallID: p.ChildID, Title: title, AgentType: p.AgentType, Model: p.Model, Resumed: p.Resumed}}}
			agents[p.ChildID] = acc
			agentOrder = append(agentOrder, acc)
		case "subagent_update", "subagent_done":
			var p agent.SubagentPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			acc := agents[p.ChildID]
			if acc == nil {
				continue
			}
			a := acc.item.Agent
			if p.Model != "" {
				a.Model = p.Model
			}
			if p.Usage != nil {
				u := *p.Usage
				a.Usage = &u
			}
			if p.ToolUses > 0 {
				a.ToolUses = p.ToolUses
			}
			if ev.Kind == "subagent_done" {
				if p.Background {
					acc.launched = true
				} else {
					d := p
					acc.done = &d
					if p.DurationMs > 0 {
						a.DurationMs = p.DurationMs
					}
				}
			}
		}
	}
	// Each name's versions, oldest first, sorted once; a row then costs its newest maxVersions plus
	// its own, so one file republished across many topics stays linear.
	sorted := map[string][]Version{}
	pos := map[string]map[string]int{}
	for _, k := range docOrder {
		if _, ok := sorted[k.name]; ok {
			continue
		}
		all := make([]Version, 0, len(versions[k.name]))
		for _, dv := range versions[k.name] {
			all = append(all, dv.v)
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].Version != all[j].Version {
				return all[i].Version < all[j].Version
			}
			return all[i].ArtifactID < all[j].ArtifactID
		})
		sorted[k.name], pos[k.name] = all, make(map[string]int, len(all))
		for i, v := range all {
			pos[k.name][v.ArtifactID] = i
		}
	}
	for _, k := range docOrder {
		ev, p := shown[k], shownP[k]
		all := sorted[k.name]
		doc := &Document{Name: k.name, View: p.View, Mock: p.View == "html" || p.View == "image", ContentType: p.ContentType, Size: p.Size,
			LatestVersion: all[len(all)-1].Version, VersionsTotal: len(all)}
		cut := max(len(all)-maxVersions, 0)
		var pick []int
		for id := range inTopic[k] {
			if i := pos[k.name][id]; i < cut {
				pick = append(pick, i)
			}
		}
		sort.Ints(pick)
		for i := cut; i < len(all); i++ {
			pick = append(pick, i)
		}
		doc.Versions = make([]Version, 0, len(pick))
		for _, i := range pick {
			v := all[i]
			v.InTopic = inTopic[k][v.ArtifactID]
			doc.Versions = append(doc.Versions, v)
		}
		k.t.Items = append(k.t.Items, Item{Type: "document", Seq: ev.Seq, EventID: ev.ID, Ts: ev.Ts, Document: doc})
	}
	for _, acc := range agentOrder {
		acc.item.Status = acc.status(opt.Live)
		if !opt.ShowTokens {
			acc.item.Usage = nil
		}
		acc.topic.Items = append(acc.topic.Items, acc.item)
	}
}

// finish orders a topic's rows by seq and counts them.
func finish(t *Topic) {
	sort.SliceStable(t.Items, func(i, j int) bool { return t.Items[i].Seq < t.Items[j].Seq })
	t.Counts = Counts{}
	for _, it := range t.Items {
		switch it.Type {
		case "document":
			t.Counts.Documents++
		case "agent":
			t.Counts.Agents++
		case "done":
			t.Counts.Commits += len(it.Commits)
		}
	}
	if t.Items == nil {
		t.Items = []Item{}
	}
}
