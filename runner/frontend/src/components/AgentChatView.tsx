import SessionSettings from './SessionSettings'
import { isFromParentSession, type ChatRefs, type FiledCard, type Ticket } from '@blerglab/chat'
// Chat/timeline view for agent-kind sessions: the runner's page over @blerglab/chat's ChatView.
// The package draws the transcript, composer, files and viewer; this file adds what is the
// runner's own — the model and effort pickers, the Rules panel, the ready card's repo line — and
// wires the package to the runner's websocket and token. The chat's settings (Show thinking,
// Compact tool calls), its Files and the Skills & plugins are in the session panel (SessionRail).
import { useNavigate } from 'react-router-dom'
import { memo, useMemo, useState, type ReactNode } from 'react'
import {
  ChatView,
  CommitViewerHost,
  HovercardProvider,
  McpHoldLine,
  ReadyCard,
  type CardTarget,
  type McpHold,
  type ChecklistItem,
  type SessionMeta,
  type DescribeUserMessage,
} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import * as ws from '../ws'
import { blergTransport } from '../lib/chatTransport'
import { boardCardUrl, useBoardOrigin } from '../lib/boardLink'
import { cardPreview, signedInAccount } from '../lib/liveBoardCard'
import { boardCardsReader } from '../lib/cardRefs'
import { useLoadSessionBoard } from '../lib/sessionBoard'
import { PinnedTicketModal } from './BoardCardView'
import ProposalEditSheet from './ProposalEditSheet'
import { ENGINE_LABELS, RUNTIME_LABELS } from '../lib/runtimes'
import { repoDetail } from '../lib/noRepo'
import type { SessionInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import {SessionPluginChatRequests} from './SessionPlugins'

// The ?artifact=<id> of the address: the link `blerg-runner publish` prints.
function artifactParam(): string | null {
  return new URLSearchParams(window.location.search).get('artifact')
}

// Takes ?artifact= out of the address (history.replaceState: no navigation, no new entry), so a
// reload does not open the viewer again.
function clearArtifactParam() {
  const u = new URL(window.location.href)
  if (!u.searchParams.has('artifact')) return
  u.searchParams.delete('artifact')
  window.history.replaceState(window.history.state, '', u.pathname + u.search + u.hash)
}

interface Props {
  transcript?: (content: ReactNode) => ReactNode
  session: SessionInfo
  /** Whether the model's thinking is drawn (the panel's Show thinking). */
  thinking: boolean
  /** Whether runs of tool calls fold into one row (the panel's Compact tool calls). */
  compact: boolean
  /** A file the History tab asked to open; n changes with every request. A diff opens at `commit`
   *  when one is given (a checklist item's sha chip). */
  openArtifact?: { id: string; n: number; commit?: string } | null
  /** The first message is held while the session's MCP servers connect (from the live state). */
  mcpHold?: McpHold | null
  checklistItems?: readonly ChecklistItem[]
  onManageChildApproval?: () => void
  onOpenChecklistItem?: (id: string) => void
  /** The viewer owns the session: "Send now" works for them, and they may open the files the agent names. */
  canReleasePrompt?: boolean
  /** The viewer may see token counts (the server's /live rule): a live reasoning count shows only then. */
  reasoningTokens?: boolean
  /** Whether the viewer may answer an owner-only approval (the server's live state); null while
   *  that is not known yet, so the owner is not told they cannot. Absent: canReleasePrompt. */
  canAnswerApprovals?: boolean | null
}

// Memoised: the page around it re-renders as the side panel's state moves, the chat need not.
export default memo(AgentChatView)

function AgentChatView({ transcript, session, thinking, compact, openArtifact, mcpHold, canReleasePrompt, reasoningTokens, canAnswerApprovals, onOpenChecklistItem, checklistItems, onManageChildApproval }: Props) {
  const boardBase = useBoardOrigin()
  const sessionId = session.id
  const navigate = useNavigate()
  const parentTitle = useSessionStore(s => s.sessions.find(p => p.id === session.parent_session_id)?.title) || session.started_by?.name
  const parentId = session.parent_session_id
  // The parent session's message or answer (the runner marks both from the parent's own token):
  // named after the parent, and a link to it inside Blerg.
  const describeUserMessage: DescribeUserMessage = (_event, payload) => {
    if (!isFromParentSession(payload)) return undefined
    const author = `↑ Parent · ${parentTitle || 'another session'}`
    if (!parentId) return { author }
    const href = `/sessions/${encodeURIComponent(parentId)}`
    return { author, authorHref: href, onAuthorOpen: () => navigate(href) }
  }
  const transport = blergTransport()
  // The board-card draft open in the editor sheet (the chat card's Edit), and a nudge for the
  // cards to read their proposal again after the sheet approved it.
  const [editingProposal, setEditingProposal] = useState<string | null>(null)
  // Card numbers and commit hashes in messages (#153): a number is a card only on the session's
  // one board, when the card exists there (its live read, cached); a hash is a commit only when the
  // session's repository says so, a read only its owner may make. The chat batches and keeps both.
  const refBoard = useLoadSessionBoard(sessionId, true)
  const [refTicket, setRefTicket] = useState<Ticket | null>(null)
  const refs = useMemo<ChatRefs>(() => ({
    cards: refBoard ? boardCardsReader(refBoard, sessionId) : undefined,
    boardCards: (boardId, numbers) => boardCardsReader({ id: boardId }, sessionId)(numbers),
    openCard: t => setRefTicket({ source: 'blerg-board', key: t.key, title: t.title ?? '', ...(t.boardId ? { board_id: t.boardId } : {}), ...(t.status ? { status: t.status } : {}) } as Ticket),
    // A pin row in the chat opens the same live dialog as the panel (#96): the Board card read live,
    // in the two-column layout; another tracker's ticket as pinned.
    openTicket: t => setRefTicket(t),
    commits: canReleasePrompt,
  }), [refBoard, sessionId, canReleasePrompt])
  const [proposalRevision, setProposalRevision] = useState(0)
  // The server's per-file limit (initial_state): the chat refuses a larger attachment before sending it.
  const maxFileBytes = useSessionStore(s => s.maxFileBytes)
  const [linked, setLinked] = useState<string | null>(artifactParam)
  const [linkedCommit, setLinkedCommit] = useState<string | null>(null)
  // A request from the History tab, taken once: the chat consumes the link when its viewer opens it.
  const [askSeen, setAskSeen] = useState(openArtifact?.n ?? 0)
  if (openArtifact && openArtifact.n !== askSeen) {
    setAskSeen(openArtifact.n)
    setLinked(openArtifact.id)
    setLinkedCommit(openArtifact.commit ?? null)
  }
  const meta: SessionMeta = session
  const engine = ENGINE_LABELS[session.engine || 'claude'] ?? session.engine
  const runtime = RUNTIME_LABELS[session.runtime || 'daemon'] ?? session.runtime
  const slots = useMemo(() => ({
    transcript,
    header: session.archived_at ? <span role="status">Archived session · read only</span> : undefined,
    aboveComposer: session.archived_at ? undefined : <>
      {mcpHold && <McpHoldLine hold={mcpHold} onSendNow={canReleasePrompt ? () => { ws.send({ type: 'release_held_prompt', session_id: sessionId }) } : undefined} />}
      <SessionPluginChatRequests sessionId={sessionId} active={canReleasePrompt === true}/>
    </>,
    readyCard: (m: SessionMeta, readyAt: number) => (
      <ReadyCard meta={{ ...m, engine, runtime }} readyAt={readyAt}>
        <div><dt>Repo</dt><dd data-testid="ready-repo">{repoDetail(session.repo)}</dd></div>
      </ReadyCard>
    ),
  }), [transcript, session.archived_at, session.repo, sessionId, engine, runtime, mcpHold, canReleasePrompt])

  // A Blerg Board card's hovercard in the chat (a pinned ticket) reads the card as the board has it.
  const hover = useMemo(() => ({ card: (t: CardTarget) => cardPreview(t, sessionId), repo: () => session.repo, account: signedInAccount }), [sessionId, session.repo])
  return (
    <HovercardProvider value={hover}>
    <SessionSettings session={session} commandsOnly />
    <CommitViewerHost files={transport.files} repo={() => session.repo}>
    <ChatView
      session={sessionId}
      transport={transport}
      readOnly={!!session.archived_at}
      checklistItems={checklistItems}
      onOpenChecklistItem={onOpenChecklistItem}
      onManageChildApproval={session.archived_at ? undefined : onManageChildApproval}
      onOpenSession={id => navigate(`/sessions/${encodeURIComponent(id)}`)}
      describeUserMessage={describeUserMessage}
      meta={meta}
      slots={slots}
      thinking={thinking ? 'shown' : 'hidden'}
      compact={compact}
      filesButton={false}
      composerAutoFocus={false}
      canAnswerApprovals={canAnswerApprovals === undefined ? canReleasePrompt === true : canAnswerApprovals}
      canOpenWorkspaceFiles={!session.archived_at && canReleasePrompt === true}
      reasoningTokens={reasoningTokens === true}
      maxFileBytes={maxFileBytes ?? undefined}
      linkedArtifact={linked}
      linkedCommit={linkedCommit}
      onLinkedArtifactConsumed={() => { setLinked(null); setLinkedCommit(null); clearArtifactParam() }}
      onEditProposal={session.archived_at ? undefined : setEditingProposal}
      onOpenBoardCard={card => openBoardCard(card, boardBase)}
      onManagePermissions={session.archived_at ? undefined : onManageChildApproval}
      proposalRevision={proposalRevision}
      refs={refs}
    />
    {refTicket && <PinnedTicketModal ticket={refTicket} sessionId={sessionId} onClose={() => setRefTicket(null)} />}
    {editingProposal && <ProposalEditSheet proposalId={editingProposal} onClose={() => setEditingProposal(null)} onDecided={() => setProposalRevision(n => n + 1)} />}
    </CommitViewerHost>
    </HovercardProvider>
  )
}

// A created card opens on its Board page, in Blerg, in a tab of its own.
function openBoardCard(card: FiledCard, boardBase: string) {
  window.open(boardCardUrl(card.board_id, card.number, boardBase), '_blank', 'noopener')
}
