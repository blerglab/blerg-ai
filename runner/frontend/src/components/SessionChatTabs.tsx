import { SessionBoardHost } from './SessionBoardTabs'
import { Activity, useCallback, useEffect, useMemo, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranscriptStore, ViewerHostContext, type ViewerHostValue, type Ticket } from '@blerglab/chat'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionLive } from '../hooks/useSessionLive'
import { useIsMobile } from '../hooks/useIsMobile'
import AgentChatView from './AgentChatView'
import { useCompact, useThinking } from '../lib/sessionRail'
import { sessionShown } from '../lib/sessionRetention'
import { tokenSubject } from '../claims'
import { getAccessToken } from '../authClient'
import { send } from '../ws'

export default function SessionChatTabs({ tabs }: { tabs: SessionTabs }) {
  // A viewer keeps its originating chat mounted even after that chat's tab closes.
  const sessions = new Set(tabs.tabs.flatMap(tab => tab.view.kind === 'session-chat' ? [tab.view.sessionId] : tab.view.ownerSessionId ? [tab.view.ownerSessionId] : []))
  return [...sessions].map(sessionId => {
    const tabId = `session-chat:${sessionId}`
    return createPortal(<SideChat tabs={tabs} sessionId={sessionId} visible={tabs.visible.includes(tabId)} tabId={tabId} onOutput={tabs.chatOutput} onKeep={tabs.keep} />, tabs.targetFor(tabId), tabId)
  })
}
function SideChat({ tabs, sessionId, visible, tabId, onOutput, onKeep }: { tabs: SessionTabs; sessionId: string; visible: boolean; tabId: string; onOutput(id: string, signature: string): void; onKeep(id: string): void }) {
  const mobile = useIsMobile()
  const sent = useCallback(() => onKeep(tabId), [onKeep, tabId])
  const output = useTranscriptStore(s => { const transcript = s.sessions[sessionId]; return transcript?.replayDone ? String(transcript.events.at(-1)?.seq ?? '') : '' })
  useEffect(() => { if (output) onOutput(tabId, output) }, [output, onOutput, tabId])
  const session = useSessionStore(s => s.sessions.find(item => item.id === sessionId))
  useEffect(() => sessionShown(tokenSubject(getAccessToken()), sessionId), [sessionId])
  const live = useSessionLive(sessionId, !!session, session?.status)
  const [thinking] = useThinking()
  const [compact] = useCompact()
  const available = !!session
  useEffect(() => { if (visible && available) send({ type: 'mark_session_read', session_id: sessionId }) }, [visible, available, sessionId])
  const transcript = useCallback((content: ReactNode) => <Activity mode={visible ? 'visible' : 'hidden'}><div className="session-transcript-pane">{content}</div></Activity>, [visible])
  const host = useMemo<ViewerHostValue>(() => ({
    ...tabs.host,
    autoFocus: false,
    chatTarget: undefined,
    active: tabs.active === tabId ? 'chat' : tabs.active,
    visible: tabs.visible.map(id => id === tabId ? 'chat' : id),
    views: tabs.host.views.filter(view => tabs.tabs.find(tab => tab.id === view.id)?.view.ownerSessionId === sessionId),
    open: (view, keep) => { tabs.keep(tabId); tabs.open({ ...view, ownerSessionId: sessionId }, keep) },
    agentOutput: undefined,
  }), [tabs, tabId, sessionId])
  const openTicket = useCallback((ticket: Ticket) => { tabs.keep(tabId); tabs.open({ kind: 'ticket', ticket, sessionId, label: ticket.key }, true) }, [tabs, tabId, sessionId])
  // Existing desktop tabs stay readable when resizing to a phone; new phone requests
  // from the conversation still use its modal fallback.
  const showingViewer = host.views.some(view => tabs.visible.includes(view.id))
  return <ViewerHostContext.Provider value={mobile && !showingViewer ? null : host}><SessionBoardHost tabs={tabs} sessionId={sessionId} />{session ? <AgentChatView onOpenTicket={mobile ? undefined : openTicket} onMessageSent={sent} sessionCommands={false} readArtifactLink={visible} session={session} transcript={transcript} thinking={thinking} compact={compact} checklistItems={live.source === 'server' ? live.live.checklist?.items : undefined} mcpHold={live.live.mcpHold ?? null} canReleasePrompt={live.source === 'loading' ? undefined : live.live.canReleasePrompt === true} canAnswerApprovals={live.source === 'loading' ? null : live.live.canReleasePrompt === true} reasoningTokens={live.live.reasoningTokens === true} /> : <p>This workspace is unavailable.</p>}</ViewerHostContext.Provider>
}
