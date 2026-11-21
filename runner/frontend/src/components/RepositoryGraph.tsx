import { IconButton, CommitViewer, visibleText, ReferenceText, ReferenceScope, type ChatRefs, type Ticket } from '@blerglab/chat'
import { useMemo, useState, type ReactNode } from 'react'
import { boardCardsReader } from '../lib/cardRefs'
import { useLoadSessionBoard } from '../lib/sessionBoard'
import { PinnedTicketModal } from './BoardCardView'
import { blergTransport } from '../lib/chatTransport'
import { graphRows, releaseRanges, unmergedRefs, type BranchGraph } from '../lib/branchGraph'

const colors = ['var(--blaze)', 'var(--chart-git)', 'var(--chart-read)', 'var(--chart-build)', 'var(--chart-edit)', 'var(--chart-other)']
const EMPTY = { commits: [], refs: [], more: false }
export default function RepositoryGraph({ graph, name, sessionId, depth, onMore, error, onRetry, status }: {
  status?: ReactNode;
  error?: string; onRetry?(): void; onExpanded?(open: boolean): void; expanded?: boolean; graph?: BranchGraph; name: string; sessionId: string; memberId?: string; depth: number; onMore(): void
}) {
  const board = useLoadSessionBoard(sessionId, true)
  const [ticket, setTicket] = useState<Ticket | null>(null)
  const refs = useMemo<ChatRefs>(() => ({ cards: board ? boardCardsReader(board, sessionId) : undefined, boardCards: (id, numbers) => boardCardsReader({ id }, sessionId)(numbers), openCard: target => setTicket({ source: 'blerg-board', board_id: target.boardId, key: target.key, title: target.title ?? '' }) }), [board, sessionId])
  const [release, setRelease] = useState<string | null>(null)
  const [commit, setCommit] = useState<string | null>(null)
  const [hover, setHover] = useState<number | null>(null)
  const [showMerged, setShowMerged] = useState(false)
  const openRefs = useMemo(() => unmergedRefs(graph ?? EMPTY), [graph])
  const visibleRefs = showMerged ? graph?.refs ?? [] : openRefs
  const releases = useMemo(() => releaseRanges(graph ?? EMPTY), [graph])
  const rows = useMemo(() => graphRows(graph ?? EMPTY), [graph])
  const lanes = Math.max(1, ...rows.map(row => row.width))
  const graphWidth = (lanes - 1) * 12 + 20
  const indexes = new Map(rows.map((row, index) => [row.commit.sha, index]))
  const color = (lane: number) => colors[lane % colors.length]
  const tags = graph?.tags ?? []
  const inclusion = (sha: string) => {
    const included = tags.filter(tag => tag.contains.includes(sha)).map(tag => tag.name)
    return included.length ? 'Included in ' + included.join(', ') : 'No inclusion proven in this history window'
  }
  return <ReferenceScope sessionId={`graph:${sessionId}`} refs={refs}><div className="repository-graph">
    {error && <p className="workspace-note" role="alert">{error} {onRetry && <IconButton label="Retry history" onClick={onRetry} />}</p>}
    {graph ? <>
      <div className="repository-graph-summary"><p className="workspace-note" title="Ancestry covers the loaded history. Load more to extend it.">{graph.commits.length} {graph.commits.length === 1 ? 'commit' : 'commits'} · {visibleRefs.length} {visibleRefs.length === 1 ? 'branch' : 'branches'} · {tags.length} {tags.length === 1 ? 'tag' : 'tags'}</p>{status}{onRetry && <IconButton label="Refresh history" onClick={onRetry} />}
        <label className="repository-graph-merged" title="Hide only branches proven merged into the default branch in loaded history"><input type="checkbox" checked={showMerged} onChange={event => setShowMerged(event.target.checked)} />Show merged</label>
      </div>
      {!!releases.length && <div className="repository-release-landmarks" aria-label="Release landmarks">{releases.map(({ tag, previous, commits }) => {
        return <details className="repository-release" key={tag.name} open={release === tag.name}>
          <summary onClick={event => { event.preventDefault(); setRelease(current => current === tag.name ? null : tag.name) }} title={visibleText(tag.name)}><strong>{visibleText(tag.name)}</strong><span>{commits.length} {commits.length === 1 ? 'commit' : 'commits'}{previous ? ' since ' + visibleText(previous.name) : ''}</span></summary>
          <div className="repository-release-detail" role="region" aria-label={`Release ${tag.name}`}>
            <ul>{commits.map(commit => <li key={commit.sha}><button type="button" className="repository-graph-commit" onClick={() => setCommit(commit.sha)}>{visibleText(commit.subject)}</button></li>)}</ul>
          </div>
        </details>
      })}</div>}
      <div className="repository-graph-scroll">
        <div className="repository-graph-canvas" style={{ gridTemplateColumns: `${graphWidth}px minmax(0, 1fr)` }}>
          <svg className="repository-graph-lines" aria-label="Branch connections" width={graphWidth} height={rows.length * 48}>
            {rows.map((row, index) => <g key={row.commit.sha}>
              {row.edges.map((edge, i) => <path key={i} d={`M ${10 + edge.from * 12} ${index * 48 + 24} C ${10 + edge.from * 12} ${index * 48 + 48} ${10 + edge.to * 12} ${(indexes.get(row.commit.parents[i]) ?? rows.length) * 48} ${10 + edge.to * 12} ${(indexes.get(row.commit.parents[i]) ?? rows.length) * 48 + 24}`} fill="none" stroke={color(edge.to)} strokeWidth={hover === edge.to ? 3 : 2} opacity={hover === null || hover === edge.to ? 1 : 0.2} />)}
              <circle cx={10 + row.lane * 12} cy={index * 48 + 24} r="3" fill="var(--basalt)" stroke={color(row.lane)} strokeWidth="2" />
            </g>)}
          </svg>
          <ol className="repository-graph-rows" aria-label={`Commit history for ${name}`}>
            {rows.map(row => <li key={row.commit.sha} onMouseEnter={() => setHover(row.lane)} onMouseLeave={() => setHover(null)}>
              <div className="repository-graph-refs">{visibleRefs.filter(ref => ref.sha === row.commit.sha).map(ref => <span className="repository-graph-ref" style={{ borderColor: color(row.lane) }} title={`${visibleText(ref.name)} · ${inclusion(ref.sha)}`} key={ref.name}>{visibleText(ref.name)}</span>)}{tags.filter(tag => tag.sha === row.commit.sha).map(tag => <span className="repository-graph-ref" key={tag.name} title={visibleText(tag.name)}>◇ {visibleText(tag.name)}</span>)}</div>
              <div className="repository-graph-subject" title={`${visibleText(row.commit.subject)} · ${inclusion(row.commit.sha)}`}>
                <button type="button" className="repository-diff-link" aria-label={`${row.commit.sha.slice(0, 7)} ${visibleText(row.commit.subject)}`} title="Open commit diff" onClick={() => setCommit(row.commit.sha)}>±</button>
                <span className="repository-subject-text" onClick={() => setCommit(row.commit.sha)}><ReferenceText text={visibleText(row.commit.subject)} /></span>
              </div>
              <code title={inclusion(row.commit.sha)}>{row.commit.sha.slice(0, 7)}</code><time className="repository-graph-time" dateTime={row.commit.date} title={row.commit.date}>{relativeTime(row.commit.date)}</time>
            </li>)}
          </ol>
        </div>
      </div>
      {!rows.length && <p className="workspace-note">No commits yet.</p>}
      {graph.more && <p>{depth < 200 ? <button type="button" className="settings-action btn small" onClick={onMore}>More history</button> : 'History limit reached.'}</p>}
      {graph.refs_limited && <p className="workspace-note">Showing up to 64 recently updated local branches, including the default branch. Older or unreadable branches were omitted.</p>}
    </> : !error && <p className="workspace-note">Loading branch history…</p>}
    {commit && <CommitViewer sessionId={sessionId} sha={commit} member={graph?.member_id && graph.generation && graph.launch_generation ? { member_id: graph.member_id, generation: graph.generation, launch_generation: graph.launch_generation } : undefined} files={blergTransport().files!} onClose={() => setCommit(null)} />}
    {ticket && <PinnedTicketModal ticket={ticket} sessionId={sessionId} onClose={() => setTicket(null)} />}
  </div></ReferenceScope>
}

function relativeTime(date?: string): string {
  const time = Date.parse(date ?? '')
  if (!Number.isFinite(time)) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000))
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago'
  if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago'
  return Math.floor(seconds / 86400) + 'd ago'
}
