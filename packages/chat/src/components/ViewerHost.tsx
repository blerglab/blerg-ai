import { createContext, memo, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useKeptScroll } from '../hooks/useKeptScroll'
import { createPortal } from 'react-dom'
import { findLatest } from '../model/artifacts'
import type { ArtifactInfo, TransportFiles, WorkspaceGitRef } from '../transport/types'
import ArtifactViewer from './ArtifactViewer'
import WorkspaceFileViewer from './WorkspaceFileViewer'
import CommitViewer from './CommitViewer'

/** Serializable view identities let a host keep, reorder, and restore its own tabs. */
export type HostedView =
  | { kind: 'artifact'; artifact: ArtifactInfo; commit?: string | null; review?: boolean; autoplay?: boolean }
  | { kind: 'workspace'; path: string; line?: number }
  | { kind: 'commit'; sha: string; sessionId?: string; member?: WorkspaceGitRef }
export interface ViewerHostValue {
  open(view: HostedView, keep?: boolean): void
  views: readonly { id: string; view: HostedView; preview?: boolean }[]
  /** False when the host restores focus across sessions and owns tab activation. */
  autoFocus?: boolean
  active: string
  target: HTMLElement | null
  /** Split hosts supply stable per-tab containers and the visible tab in each group. */
  targetFor?(id: string): HTMLElement
  chatTarget?: HTMLElement
  visible?: readonly string[]
  close(id: string): void
  keep(id: string): void
  agentOutput?(signature: string): void
}
export const ViewerHostContext = createContext<ViewerHostValue | null>(null)
export function useViewerHost() { return useContext(ViewerHostContext) }

/** Hand a legacy modal request to the host after commit, then consume it once. */
export function HostedViewerRequest({ view, consumed }: { view: HostedView; consumed: () => void }) {
  const host = useViewerHost()
  const handled = useRef(false)
  useEffect(() => { if (!host || handled.current) return; handled.current = true; host.open(view); consumed() }, [host, view, consumed])
  return null
}

/** Render within the chat providers so review/markup retain their send and approval context. */
export const HostedViewers = memo(function HostedViewers({ sessionId, files, repo, artifacts = [], onSaved }: { sessionId: string; files: TransportFiles; repo?: string; artifacts?: readonly ArtifactInfo[]; onSaved?: () => void }) {
  const host = useViewerHost()
  const [visited, setVisited] = useState<{ session: string; ids: string[] }>({ session: sessionId, ids: [] })
  if (visited.session !== sessionId) setVisited({ session: sessionId, ids: [] })
  else if (host && (host.visible ?? [host.active]).some(id => id !== 'chat' && !visited.ids.includes(id))) setVisited({ session: sessionId, ids: [...new Set([...visited.ids, ...(host.visible ?? [host.active]).filter(id => id !== 'chat')])] })
  if (!host || (!host.target && !host.targetFor)) return null
  return <>{host.views.filter(tab => (host.visible ?? [host.active]).includes(tab.id) || visited.session === sessionId && visited.ids.includes(tab.id)).map(({ id, view }) => {
    const visible = (host.visible ?? [host.active]).includes(id)
    let body: ReactNode
    const common = { sessionId, files, embedded: true, onClose: () => host.close(id) }
    if (view.kind === 'workspace') body = <WorkspaceFileViewer {...common} path={view.path} line={view.line} onSaved={onSaved} />
    else if (view.kind === 'commit') body = <CommitViewer {...common} sessionId={view.sessionId ?? sessionId} sha={view.sha} member={view.member} repo={repo} />
    else body = <ArtifactViewer {...common} artifact={artifacts.find(a => a.id === view.artifact.id) ?? view.artifact} latest={findLatest([...artifacts], view.artifact)} commit={view.commit} review={view.review} autoplay={view.autoplay && visible} onOpen={artifact => host.open({ kind: 'artifact', artifact })} />
    return createPortal(<KeptView files={files} sessionId={sessionId} id={id} visible={visible} onKeep={() => host.keep(id)}>{body}</KeptView>, host.targetFor?.(id) ?? host.target!, id)
  })}</>
})

/** One tab's view: its scroll positions kept per session and tab across visits (#281). */
function KeptView({ files, sessionId, id, visible, onKeep, children }: { files: TransportFiles; sessionId: string; id: string; visible: boolean; onKeep: () => void; children: ReactNode }) {
  const root = useRef<HTMLDivElement>(null)
  useKeptScroll(root, { scope: files, sessionId, key: `tab:${id}` }, visible)
  return <div ref={root} className="hosted-view" hidden={!visible} onDoubleClick={onKeep} onClickCapture={onKeep} onKeyDownCapture={e => { if (!e.defaultPrevented && !['Tab', 'Escape', 'Shift', 'Control', 'Meta', 'Alt'].includes(e.key)) onKeep() }}>{children}</div>
}
