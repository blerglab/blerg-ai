// A History reply for the panel's tests: an open topic, a finished one with two documents (one with
// a deleted and a later out-of-topic version), three subagents and three commits (two linked, one
// with no known repository), and the unmarked work: two files and five subagents no marker holds.
import type { AgentItem, Stroke, Topic, TopicsReply, Unmarked } from '../lib/topics'

export function reelReply(now = new Date()): TopicsReply {
  const at = (minAgo: number) => new Date(now.getTime() - minAgo * 60_000).toISOString()
  const open: Topic = {
    id: 'tp_open', source: 'marker', title: 'Fix the drawer on phones', status: 'open', start_seq: 300, end_seq: 340,
    started_at: at(20), ended_at: null, counts: { agents: 1 },
    items: [
      { type: 'request', seq: 300, event_id: 'ev-300', ts: at(20), text: 'Fix the drawer on phones', marker: true },
      { type: 'agent', seq: 320, event_id: 'ev-320', ts: at(15), call_id: 'toolu_9', title: 'Reproduce at 390 px', agent_type: 'Explore',
        model: 'claude-haiku-5', usage: { input_tokens: 12000, output_tokens: 400 }, tool_uses: 9, duration_ms: 0, status: 'running' },
    ],
  }
  const done: Topic = {
    id: 'tp_done', source: 'marker', title: 'Build the History tab', status: 'done', outcome: 'Shipped on main',
    start_seq: 100, end_seq: 290, started_at: at(120), ended_at: at(72), counts: { documents: 2, agents: 3, commits: 3 },
    items: [
      { type: 'request', seq: 100, event_id: 'ev-100', ts: at(120), text: 'Build the History tab per the spec' },
      { type: 'document', seq: 140, event_id: 'ev-140', ts: at(110), name: 'plan.md', view: 'markdown', mock: false,
        content_type: 'text/markdown', size: 2048, latest_version: 3, versions_total: 3, versions: [
          { version: 1, artifact_id: 'a1', created_at: at(115), in_topic: true, deleted: true },
          { version: 2, artifact_id: 'a2', created_at: at(110), in_topic: true, deleted: false },
          { version: 3, artifact_id: 'a3', created_at: at(30), in_topic: false, deleted: false },
        ] },
      { type: 'document', seq: 150, event_id: 'ev-150', ts: at(105), name: 'mock.html', view: 'html', mock: true,
        content_type: 'text/html', size: 900, latest_version: 1, versions_total: 1, versions: [
          { version: 1, artifact_id: 'h1', created_at: at(105), in_topic: true, deleted: false },
        ] },
      { type: 'agent', seq: 160, event_id: 'ev-160', ts: at(100), call_id: 'toolu_1', title: 'Implement task 3', agent_type: 'general-purpose',
        model: 'claude-sonnet-5', usage: { output_tokens: 1500 }, tool_uses: 37, duration_ms: 412000, status: 'done' },
      { type: 'agent', seq: 170, event_id: 'ev-170', ts: at(95), call_id: 'toolu_2', title: 'Review the branch', agent_type: 'general-purpose',
        model: 'claude-sonnet-5', usage: null, tool_uses: 4, duration_ms: 30000, status: 'error' },
      { type: 'agent', seq: 180, event_id: 'ev-180', ts: at(90), call_id: 'toolu_3', title: 'Watch CI', agent_type: 'general-purpose',
        model: '', usage: null, tool_uses: 0, duration_ms: 0, status: 'stopped' },
      { type: 'done', seq: 290, event_id: 'ev-290', ts: at(72), outcome: 'Shipped on main', commits: [
        { sha: '1a2b3c4d5e6f708192a3b4c5d6e7f80912a3b4c5', url: 'https://github.com/example-org/example-repo/commit/1a2b3c4d5e6f708192a3b4c5d6e7f80912a3b4c5' },
        { sha: '9f8e7d6', url: 'https://github.com/example-org/example-repo/commit/9f8e7d6' },
        { sha: '0badc0ffee0ddf00d', url: null },
      ] },
    ],
  }
  const agent = (seq: number, minAgo: number, title: string, status: AgentItem['status'], model = '', tokens = 0): AgentItem => ({
    type: 'agent', seq, event_id: `ev-${seq}`, ts: at(minAgo), call_id: `toolu_u${seq}`, title, agent_type: 'general-purpose',
    model, usage: tokens ? { output_tokens: tokens } : null, tool_uses: 3, duration_ms: 60000, status,
  })
  const unmarked: Unmarked = {
    agents: 5, first_seq: 5, last_seq: 96, started_at: at(230), ended_at: at(122),
    documents: [
      { type: 'document', seq: 30, event_id: 'ev-30', ts: at(210), name: 'notes.md', view: 'markdown', mock: false,
        content_type: 'text/markdown', size: 640, latest_version: 2, versions_total: 2, versions: [
          { version: 1, artifact_id: 'n1', created_at: at(215), in_topic: true, deleted: false },
          { version: 2, artifact_id: 'n2', created_at: at(210), in_topic: true, deleted: false },
        ] },
      { type: 'document', seq: 60, event_id: 'ev-60', ts: at(180), name: 'drawer.png', view: 'image', mock: true,
        content_type: 'image/png', size: 4096, latest_version: 1, versions_total: 1, versions: [
          { version: 1, artifact_id: 'd1', created_at: at(180), in_topic: true, deleted: false },
        ] },
    ],
    subagents: [
      agent(5, 230, 'Survey the repo', 'done', 'claude-haiku-5', 2400),
      agent(20, 220, 'Review the sign-in guard fix', 'done', 'claude-sonnet-5', 18000),
      agent(45, 200, 'Reproduce the stuck refresh', 'error', 'claude-sonnet-5'),
      agent(70, 170, 'Watch CI', 'stopped'),
      agent(96, 122, 'Check the badge on phones', 'done', 'claude-haiku-5', 900),
    ],
  }
  return { session_id: 'sess-a', through_seq: 340, generated_at: now.toISOString(), tokens_hidden: false, unmarked, topics: [open, done] }
}

/** reelReply with the History feed: strokes in both topics and outside them, of every kind — the
 *  person's decision and the agent's own, an open question and an answered one, an approval, a
 *  proposal with its card, milestones with commits, a finding, a back-dated mark, and hostile text. */
export function strokesReply(now = new Date()): TopicsReply {
  const reply = reelReply(now)
  const at = (minAgo: number) => new Date(now.getTime() - minAgo * 60_000).toISOString()
  const s = (over: Partial<Stroke> & Pick<Stroke, 'id' | 'kind' | 'source' | 'text' | 'seq'>, minAgo: number): Stroke => ({
    at: at(minAgo), jump: { event_id: `ev-${over.seq}`, seq: over.seq }, ...over,
  })
  reply.strokes = [
    s({ id: 'st-q', kind: 'attention', source: 'ask', text: 'Which drawer width should the phone use?', state: 'open', seq: 335, topic_id: 'tp_open' }, 2),
    s({ id: 'st-f', kind: 'finding', source: 'mark', by: 'agent', text: 'The drawer covers the composer at 390 px', seq: 330, topic_id: 'tp_open' }, 5),
    s({ id: 'st-d2', kind: 'decision', source: 'mark', by: 'agent', text: 'Use the existing sheet component instead of a new one', seq: 325, topic_id: 'tp_open' }, 10),
    s({ id: 'st-x', kind: 'decision', source: 'mark', by: 'person', text: '<img src=x onerror="alert(1)"> <b>bold</b> javascript:alert(2)', seq: 296 }, 50),
    s({ id: 'st-m', kind: 'milestone', source: 'topic', text: 'Shipped on main', seq: 290, topic_id: 'tp_done',
      commits: [{ sha: '9f8e7d6', url: null }] }, 72),
    s({ id: 'st-p', kind: 'attention', source: 'proposal', by: 'person', text: 'File a board card', state: 'resolved', outcome: 'Approved', seq: 200, topic_id: 'tp_done',
      jump: { event_id: 'ev-200', proposal_id: 'p1', seq: 200 }, card: { board_id: 'b1', board_name: 'Product', number: 42, title: 'Export button is stale' } }, 85),
    s({ id: 'st-a', kind: 'attention', source: 'delegation', by: 'person', text: 'Start a Codex session: Review the diff', state: 'resolved', outcome: 'Allowed once', seq: 190, topic_id: 'tp_done' }, 88),
    s({ id: 'st-d1', kind: 'decision', source: 'mark', by: 'person', text: 'Ship the feed first, the filters next', seq: 120, topic_id: 'tp_done', card: { number: 115 } }, 115),
    s({ id: 'st-old', kind: 'milestone', source: 'mark', by: 'agent', text: 'v1.2.0 released', seq: 341, backdated: true, jump: { event_id: 'ev-1', seq: 1 },
      commits: [{ sha: '1a2b3c4d', url: null }] }, 600),
  ]
  return reply
}

/** The owner's topic: 27 board-card proposals (26 approved with their cards, 1 rejected) folded into
 *  one line, 3 subagents, 3 messages of the person's, two files and a commit; and, when waiting, the
 *  newest proposal still waiting. */
export function ownerReply(now = new Date(), opts: { waiting?: boolean; oldestWaiting?: boolean } = {}): TopicsReply {
  const at = (minAgo: number) => new Date(now.getTime() - minAgo * 60_000).toISOString()
  const proposals: Stroke[] = Array.from({ length: 27 }, (_, i): Stroke => {
    const seq = 100 + i * 2
    const rejected = i === 13
    const waiting = (opts.waiting && i === 26) || (opts.oldestWaiting && i === 0)
    return {
      id: `ev-p${i}`, kind: 'attention', source: 'proposal', by: waiting ? undefined : 'person', text: 'File a board card',
      state: waiting ? 'open' : rejected ? 'declined' : 'resolved', outcome: waiting ? undefined : rejected ? 'Rejected' : 'Approved',
      at: at(60 - i), seq, topic_id: 'tp_owner', jump: { event_id: `ev-p${i}`, proposal_id: `p${i}`, seq },
      ...(rejected || waiting ? {} : { card: { board_id: 'b1', board_name: 'Blerg', number: 44 + i } }),
    }
  }).reverse()
  const cards = proposals.filter(p => p.card).map(p => p.card!).reverse()
  const fold: Stroke = {
    id: 'fold:ev-p0', kind: 'attention', source: 'proposal', text: `Filed ${cards.length} cards`,
    state: opts.waiting || opts.oldestWaiting ? 'open' : 'resolved', outcome: opts.waiting || opts.oldestWaiting ? '1 waiting, 25 approved, 1 rejected' : 'approved, 1 rejected',
    at: proposals[0].at, first_at: proposals[26].at, seq: proposals[0].seq, topic_id: 'tp_owner',
    jump: proposals[0].jump, count: 27, items: proposals, cards,
  }
  const agent = (seq: number, title: string, status: AgentItem['status'], tokens: number, ms: number): AgentItem => ({
    type: 'agent', seq, event_id: `ev-${seq}`, ts: at(70 - seq / 10), call_id: `toolu_${seq}`, title, agent_type: 'general-purpose',
    model: 'claude-sonnet-5', usage: { output_tokens: tokens }, tool_uses: 5, duration_ms: ms, status,
  })
  const topic: Topic = {
    id: 'tp_owner', source: 'marker', title: 'File the review follow-ups as cards on the 0.4.5 board', status: 'open',
    start_seq: 90, end_seq: 200, started_at: at(70), ended_at: null, counts: { documents: 2, agents: 3, commits: 1, cards: 26 },
    messages: [
      { seq: 90, event_id: 'ev-90', ts: at(70), text: 'File every follow-up from the review as a card' },
      { seq: 140, event_id: 'ev-140', ts: at(45), text: 'Skip the one about the installer' },
      { seq: 180, event_id: 'ev-180', ts: at(20), text: 'Tag them all 0.4.5' },
    ],
    items: [
      { type: 'request', seq: 90, event_id: 'ev-90', ts: at(70), text: 'File every follow-up from the review as a card' },
      agent(95, 'Read the review and list the follow-ups', 'done', 18000, 240000),
      agent(97, 'Search the board for duplicates', 'done', 9000, 60000),
      agent(99, 'Draft the card bodies', 'error', 4000, 30000),
      { type: 'document', seq: 150, event_id: 'ev-150', ts: at(40), name: 'follow-ups.md', view: 'markdown', mock: false,
        content_type: 'text/markdown', size: 4096, latest_version: 2, versions_total: 2, versions: [
          { version: 1, artifact_id: 'f1', created_at: at(50), in_topic: true, deleted: false },
          { version: 2, artifact_id: 'f2', created_at: at(40), in_topic: true, deleted: false },
        ] },
      { type: 'document', seq: 160, event_id: 'ev-160', ts: at(35), name: 'a-very-long-file-name-for-the-duplicate-search-results.csv', view: 'csv', mock: false,
        content_type: 'text/csv', size: 2048, latest_version: 1, versions_total: 1, versions: [
          { version: 1, artifact_id: 'c1', created_at: at(35), in_topic: true, deleted: false },
        ] },
      { type: 'done', seq: 199, event_id: 'ev-199', ts: at(5), outcome: '', commits: [{ sha: '1a2b3c4d', url: null }] },
    ],
  }
  return {
    session_id: 'sess-a', through_seq: 200, generated_at: now.toISOString(), tokens_hidden: false, unmarked: null, topics: [topic],
    strokes: [fold, { id: 'ev-dec', kind: 'decision', source: 'mark', by: 'person', text: 'One card per follow-up', at: at(65), seq: 92, topic_id: 'tp_owner', jump: { event_id: 'ev-dec', seq: 92 } }],
  }
}
