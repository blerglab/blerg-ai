import { onOpen as onConnectionOpen } from '../ws'
import { watchTurnEnds } from '../lib/explorerAutoRefresh'
import RepositoryStatus from './RepositoryStatus'
import { useSessionStore } from '../hooks/useSessionStore'
import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { apiFetch } from '../apiFetch'
import type { BranchGraph } from '../lib/branchGraph'
import RepositoryGraph from './RepositoryGraph'
import './DockRepositories.css'

function GraphTab({ sessionId, memberId, name, active }: { sessionId: string; memberId: string; name: string; active: boolean }) {
  const member = useSessionStore(state => state.sessions.find(session => session.id === sessionId)?.workspace?.members.find(member => member.id === memberId))
  const [depth, setDepth] = useState(50)
  const [graph, setGraph] = useState<BranchGraph>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  useEffect(() => {
    if (!active) return
    const refresh = () => setRetry(value => value + 1)
    const stop = watchTurnEnds(sessionId, refresh)
    const reconnect = onConnectionOpen(refresh)
    return () => { stop(); reconnect() }
  }, [sessionId, active])
  useEffect(() => {
    if (!active) return
    const aborter = new AbortController()
    const read = async () => {
      try {
        const query = new URLSearchParams({ member_id: memberId, depth: String(depth) })
        let response: Response
        for (let attempt = 0; ; attempt++) {
          response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/git/graph?${query}`, { signal: aborter.signal })
          if (response.status !== 429 || attempt === 3) break
          await new Promise<void>((resolve, reject) => {
            const cancel = () => { clearTimeout(timer); reject(new DOMException('Cancelled', 'AbortError')) }
            const timer = setTimeout(() => { aborter.signal.removeEventListener('abort', cancel); resolve() }, 150 * 2 ** attempt)
            aborter.signal.addEventListener('abort', cancel, { once: true })
            if (aborter.signal.aborted) cancel()
          })
        }
        if (!response.ok) {
          const problem: { error?: unknown } | null = await response.json().catch(() => null)
          throw new Error(response.status === 429 ? 'Branch history is busy. Try again.' : typeof problem?.error === 'string' && problem.error ? problem.error : 'Branch history unavailable.')
        }
        const next: BranchGraph = await response.json()
        if (!Array.isArray(next.commits) || !Array.isArray(next.refs)) throw new Error('Branch history unavailable.')
        setGraph(next); setError('')
      } catch (failure) { if (!aborter.signal.aborted) setError(failure instanceof Error ? failure.message : 'Branch history unavailable.') }
    }
    void read()
    return () => aborter.abort()
  }, [sessionId, memberId, depth, active, retry])
  return <section className="repository-focused" aria-label={`Branch graph for ${name}`}><RepositoryGraph status={member && <RepositoryStatus sessionId={sessionId} path={member.path} name={name} active={active} quiet />} name={name} sessionId={sessionId} memberId={memberId} expanded graph={graph} error={error} onRetry={() => setRetry(value => value + 1)} depth={depth} onMore={() => setDepth(value => Math.min(200, value + 50))} /></section>
}

export default function RepositoryGraphTabs({ tabs }: { tabs: SessionTabs }) {
  return tabs.tabs.map(tab => tab.view.kind === 'branch-graph' && createPortal(<GraphTab sessionId={tab.view.sessionId} memberId={tab.view.memberId} name={tab.view.label} active={tabs.visible.includes(tab.id)} />, tabs.targetFor(tab.id), tab.id))
}
