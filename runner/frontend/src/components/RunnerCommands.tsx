import RuntimeIcon from './RuntimeIcon'
import { openSessionFile } from '../lib/openSessionFile'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { usePalette, usePaletteCommands } from '../commands/context'
import { cardProvider } from '../commands/cardProvider'
import { PinnedTicketModal } from './BoardCardView'
import { workspaceProvider } from '../commands/workspaceProvider'
import { useSessionStore } from '../hooks/useSessionStore'
import { setSettingsOpen } from '../lib/settingsDialog'
import { apiFetch } from '../apiFetch'
import { blergTransport } from '../lib/chatTransport'
import { WorkspaceFileViewer, type Ticket } from '@blerglab/chat'
import type { PaletteCommand, PaletteProvider } from '../commands/registry'
import { useBoardOrigin, loadBoardOrigin } from '../lib/boardLink'
import { signedInAccount } from '../lib/liveBoardCard'
import { boardPageUrl } from '../lib/sessionBoardLinks'
import { recentSessionIds, recentSessionOrder, rememberSessionVisit } from '../lib/recentSessions'

export default function RunnerCommands({ onNewSession }: { onNewSession(): void }) {
  const navigate = useNavigate()
  const boardOrigin = useBoardOrigin()
  const [card, setCard] = useState<{ ticket: Ticket; account: string } | null>(null)
  const location = useLocation()
  const palette = usePalette()
  const sessions = useSessionStore(s => s.sessions)
  const sessionId = location.pathname.match(/^\/sessions\/([^/]+)\/?$/)?.[1]
  const session = sessions.find(s => s.id === sessionId)
  const activeSessionId = session?.id
  const account = signedInAccount()
  // Read on every render: a visit recorded by the effect below shows the next time the palette opens.
  const visited = recentSessionIds(account)
  const visitedKey = visited.join(',')
  const offered = useMemo(() => new Map(recentSessionOrder(sessions, visitedKey ? visitedKey.split(',') : [], sessionId).map((id, i) => [id, i])), [sessions, visitedKey, sessionId])
  const [viewing, setViewing] = useState<{ session: string; path: string; account: string } | null>(null)
  const items = useMemo<PaletteCommand[]>(() => [
    { id: 'new-session', destructive: false, title: 'New session', keywords: 'Start a new session', empty: true, run: onNewSession },
    { id: 'settings', destructive: false, title: 'Settings', keywords: 'Open Settings', empty: true, run: () => setSettingsOpen(true) },
    { id: 'open-board', destructive: false, title: 'Open board', empty: true, here: session ? 5 : undefined, run: () => { window.open(`${boardOrigin}/boards`, '_blank', 'noopener') } },
    ...['Sessions', 'Insights', 'Crons', 'Proposals'].map(title => ({ id: `page:${title}`, destructive: false, title: `Open ${title}`, run: () => navigate(`/${title.toLowerCase()}`) })),
    // Search finds every other session (archived, ended, cron runs); the empty palette offers only
    // the ones in `offered`. The session in view is never offered: it is already open.
    ...sessions.filter(s => s.id !== sessionId).map(s => ({ id: `session:${s.id}`, destructive: false, title: s.title, group: 'sessions' as const, repo: s.repo, recentAt: s.started_at, recentRank: offered.get(s.id), icon: <RuntimeIcon runtime={s.runtime} machine={s.daemon_id} />, detail: s.archived_at ? `${s.status} · archived` : s.status, run: () => navigate(`/sessions/${encodeURIComponent(s.id)}`) })),
    ...(session ? [{ id: `quick-open:${session.id}`, destructive: false, title: 'Quick open session files', shortcut: 'Ctrl/⌘+P', here: 1, run: () => palette?.open('files') }] : []),
  ], [navigate, onNewSession, palette, sessions, session, sessionId, boardOrigin, offered])
  const providers = useMemo<PaletteProvider[]>(() => [
    cardProvider(ticket => setCard({ ticket, account: signedInAccount() })),
    {
      id: 'boards', mode: 'commands',
      async search(_query, signal) {
        const account = signedInAccount()
        const response = await apiFetch('/api/session-boards', { signal })
        if (!response.ok) throw new Error('Could not load boards. Try again.')
        const reply = await response.json() as { boards: { id: string; name: string }[] }
        const origin = await loadBoardOrigin()
        signal.throwIfAborted()
        if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
        const boards = reply.boards ?? []
        return { items: boards.map(board => ({
          id: `board:${board.id}`, destructive: false, title: `Open board: ${board.name}`,
          run: () => { window.open(boardPageUrl(origin, board.id), '_blank', 'noopener') },
        })) }
      },
    },
    ...(activeSessionId ? [workspaceProvider(activeSessionId, path => { const request = { session: activeSessionId, path, account: signedInAccount() }; if (!openSessionFile(request)) setViewing(request) })] : []),
  ], [activeSessionId])
  usePaletteCommands(items, providers)
  useEffect(() => {
    if (!sessionId) return
    palette?.registry.remember(`session:${sessionId}`)
    rememberSessionVisit(account, sessionId)
  }, [palette, sessionId, account])
  const files = blergTransport().files
  return <>{card && card.account === signedInAccount() && <PinnedTicketModal ticket={card.ticket} sessionId={sessionId ?? ''} onClose={() => setCard(null)} />}
  {viewing && viewing.account === signedInAccount() && viewing.session === session?.id && session && files ? <WorkspaceFileViewer key={`${session.id}:${viewing.path}`} sessionId={session.id} files={files} path={viewing.path} onClose={() => setViewing(null)} /> : null}</>
}
