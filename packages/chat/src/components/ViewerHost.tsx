import { createContext, memo, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useKeptScroll } from '../hooks/useKeptScroll'
import { createPortal } from 'react-dom'
import { findLatest } from '../model/artifacts'
import type { CardRegistry } from '../cards/registry'
import type { ArtifactInfo, TransportFiles, WorkspaceGitRef } from '../transport/types'
import ArtifactViewer from './ArtifactViewer'
import WorkspaceFileViewer from './WorkspaceFileViewer'
import CommitViewer from './CommitViewer'
import MessageViewer from './MessageViewer'
import VisualViewer from './VisualViewer'
import type { VisualKind } from '../model/visualKind'

/** Serializable view identities let a host keep, reorder, and restore its own tabs. */
export type HostedView =
  | { kind: 'artifact'; artifact: ArtifactInfo; commit?: string | null; review?: boolean; autoplay?: boolean }
  | { kind: 'workspace'; path: string; line?: number }
  | { kind: 'commit'; sha: string; sessionId?: string; member?: WorkspaceGitRef }
  /** A message popped out of the chat (BLG-449): a snapshot of its text at the time, keyed by the
   *  event it came from so reopening the same message focuses the same tab. workspaceLinks
   *  matches EventCards: on for an assistant reply, never for a person's own message. `kept`
   *  false (the host's own bounded snapshot store had no room) means it stays for this session
   *  but will not survive a reload; absent or true means it will. */
  | { kind: 'message'; messageId: string; text: string; label: string; workspaceLinks?: boolean; kept?: boolean }
  /** An inline chart/diagram popped out of the chat (BLG-451): a snapshot of its source at the
   *  time, keyed by a content hash of kind+source so popping out the same unchanged block twice
   *  reuses one tab. `kept` follows the same bounded snapshot store as `message` above, and the
   *  same meaning. */
  | { kind: 'visual'; id: string; visualKind: VisualKind; source: string; label: string; kept?: boolean }
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
export const HostedViewers = memo(function HostedViewers({ sessionId, files, repo, artifacts = [], onSaved, cards }: { sessionId: string; files: TransportFiles; repo?: string; artifacts?: readonly ArtifactInfo[]; onSaved?: () => void; cards?: CardRegistry }) {
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
    else if (view.kind === 'message') body = <MessageViewer text={view.text} label={view.label} embedded onClose={() => host.close(id)} cards={cards} workspaceLinks={view.workspaceLinks} kept={view.kept} />
    else if (view.kind === 'visual') body = <VisualViewer visualKind={view.visualKind} source={view.source} label={view.label} kept={view.kept} embedded onClose={() => host.close(id)} />
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
