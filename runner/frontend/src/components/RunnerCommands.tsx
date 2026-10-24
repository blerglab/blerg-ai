import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { usePalette, usePaletteCommands } from '../commands/context'
import { workspaceProvider } from '../commands/workspaceProvider'
import { useSessionStore } from '../hooks/useSessionStore'
import { setSettingsOpen } from '../lib/settingsDialog'
import { apiFetch } from '../apiFetch'
import { blergTransport } from '../lib/chatTransport'
import { WorkspaceFileViewer } from '@blerglab/chat'
import type { PaletteItem, PaletteProvider } from '../commands/registry'
import { boardCardUrl, loadBoardOrigin } from '../lib/boardLink'
import { signedInAccount } from '../lib/liveBoardCard'

export default function RunnerCommands({ onNewSession }: { onNewSession(): void }) {
  const navigate = useNavigate()
  const location = useLocation()
  const palette = usePalette()
  const sessions = useSessionStore(s => s.sessions)
  const sessionId = location.pathname.match(/^\/sessions\/([^/]+)\/?$/)?.[1]
  const session = sessions.find(s => s.id === sessionId)
  const activeSessionId = session?.id
  const [viewing, setViewing] = useState<{ session: string; path: string; account: string } | null>(null)
  const items = useMemo<PaletteItem[]>(() => [
    { id: 'new-session', title: 'Start a new session', run: onNewSession },
    { id: 'settings', title: 'Open Settings', run: () => setSettingsOpen(true) },
    ...['Sessions', 'Insights', 'Crons', 'Proposals', 'Boards'].map(title => ({ id: `page:${title}`, title: `Open ${title}`, run: () => navigate(`/${title.toLowerCase()}`) })),
    ...sessions.filter(s => !s.archived_at && s.id !== sessionId).map(s => ({ id: `session:${s.id}`, title: `Switch to session: ${s.title}`, detail: s.repo, run: () => navigate(`/sessions/${encodeURIComponent(s.id)}`) })),
    ...(session ? [{ id: `quick-open:${session.id}`, title: 'Quick open session files', shortcut: 'Ctrl/⌘+P', run: () => palette?.open('files') }] : []),
  ], [navigate, onNewSession, palette, sessions, session, sessionId])
  const providers = useMemo<PaletteProvider[]>(() => [
    {
      id: 'boards', mode: 'commands',
      async search(query, signal) {
        const account = signedInAccount()
        const response = await apiFetch('/api/session-boards', { signal })
        if (!response.ok) throw new Error('Could not load boards. Try again.')
        const reply = await response.json() as { boards: { id: string; name: string }[] }
        const origin = await loadBoardOrigin()
        signal.throwIfAborted()
        if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
        const boards = reply.boards ?? []
        const number = query.match(/#(\d+)/)?.[1]
        return { items: boards.map(board => ({
          id: `board:${board.id}:${number ?? ''}`, title: number ? `Open card #${number} on board ${board.name}` : `Open board: ${board.name}`,
          run: () => { window.location.href = number ? boardCardUrl(board.id, Number(number), origin) : `${origin}/boards/${encodeURIComponent(board.id)}` },
        })) }
      },
    },
    ...(activeSessionId ? [workspaceProvider(activeSessionId, path => setViewing({ session: activeSessionId, path, account: signedInAccount() }))] : []),
  ], [activeSessionId])
  usePaletteCommands(items, providers)
  useEffect(() => { if (sessionId) palette?.registry.remember(`session:${sessionId}`) }, [palette, sessionId])
  const files = blergTransport().files
  return viewing && viewing.account === signedInAccount() && viewing.session === session?.id && session && files ? <WorkspaceFileViewer key={`${session.id}:${viewing.path}`} sessionId={session.id} files={files} path={viewing.path} onClose={() => setViewing(null)} /> : null
}
