// A History reply for the panel's tests: an open topic, a finished one with two documents (one with
// a deleted and a later out-of-topic version), three subagents and three commits (two linked, one
// with no known repository), and the unmarked work: two files and five subagents no marker holds.
import type { AgentItem, Topic, TopicsReply, Unmarked } from '../lib/topics'

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
