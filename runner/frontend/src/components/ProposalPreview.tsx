import { Markdown } from '@blerglab/chat'
import type { ProposalInfo } from '../types'
import { formatArguments } from '../lib/proposals'
import './ProposalAttention.css'

const text = (value: unknown) => typeof value === 'string' ? value : ''
// The title the agent wrote into the request's arguments, if any: never the heading of the card.
const agentTitle = (p: ProposalInfo) => text(p.arguments.title) || text(p.arguments.subject)

// What approving runs, from the server's own fields (never from the agent's arguments): the tool
// and its connection. It is the first line of every preview so an agent-written title cannot pose
// as a description of the action.
export function ProposalAction({ proposal: p }: { proposal: ProposalInfo }) {
  return <p className="proposal-action" data-testid={`proposal-action-${p.id}`}>
    <span className="proposal-action-label">Runs</span> <code className="proposal-action-tool">{p.tool}</code>
    {' '}<span className="proposal-action-label">on</span> <strong className="proposal-action-connection">{p.connection_name || 'an unnamed connection'}</strong>
  </p>
}

// A readable projection of the saved request, never the agent's summary. Exact JSON stays available.
export default function ProposalPreview({ proposal: p, complete = false }: { proposal: ProposalInfo; complete?: boolean }) {
  const board = p.kind === 'board_file'
  const title = agentTitle(p)
  return <div className="proposal-preview">
    <ProposalAction proposal={p} />
    {board ? <article className="proposal-card-preview" aria-label="Card the agent proposes">
      <p className="proposal-agent-label">Card the agent wrote</p>
      <h3>{title || 'Untitled card'}</h3>
      <dl><dt>Type</dt><dd>{text(p.arguments.type) || 'issue'}</dd>
        <dt>Board</dt><dd>{text(p.arguments.board_name) || text(p.arguments.board_id)}</dd>
        <dt>Repository</dt><dd>{Array.isArray(p.arguments.repos) ? p.arguments.repos.map(String).join(', ') || 'None selected' : text(p.arguments.repo) || 'None selected'}</dd></dl>
      <div className="proposal-readable-body" role="region" aria-label="Proposed card body" tabIndex={0}><Markdown text={text(p.arguments.body)} /></div>
    </article> : <div className="proposal-tool-preview" role="region" aria-label="Tool request preview" tabIndex={0}>
      {title && <p className="proposal-agent-title" data-testid={`proposal-agent-title-${p.id}`}><span className="proposal-agent-label">Title in the agent’s request:</span> {title}</p>}
      <dl>{Object.entries(p.arguments).map(([key, value]) => <div key={key}>
        <dt>{key.replaceAll('_', ' ')}</dt>
        <dd>{typeof value === 'string'
          ? /^(body|description|content|message|text)$/i.test(key) ? <Markdown text={value} /> : value
          : <pre>{JSON.stringify(value, null, 2)}</pre>}</dd>
      </div>)}</dl>
    </div>}
    <details open={complete || undefined} className="proposal-exact">
      <summary>Show exact request</summary>
      <pre tabIndex={0} data-testid={`proposal-args-${p.id}`}>{formatArguments(p)}</pre>
    </details>
  </div>
}
