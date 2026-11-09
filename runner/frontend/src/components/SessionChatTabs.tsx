import { SessionBoardHost } from './SessionBoardTabs'
import { Activity, useCallback, useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranscriptStore, ViewerHostContext } from '@blerglab/chat'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionLive } from '../hooks/useSessionLive'
import AgentChatView from './AgentChatView'
import { useCompact, useThinking } from '../lib/sessionRail'
import { sessionShown } from '../lib/sessionRetention'
import { tokenSubject } from '../claims'
import { getAccessToken } from '../authClient'
import { send } from '../ws'

export default function SessionChatTabs({ tabs }: { tabs: SessionTabs }) {
  return tabs.tabs.filter(t => t.view.kind === 'session-chat').map(tab => tab.view.kind === 'session-chat' && createPortal(<SideChat tabs={tabs} sessionId={tab.view.sessionId} visible={tabs.visible.includes(tab.id)} tabId={tab.id} onOutput={tabs.chatOutput} onKeep={tabs.keep} />, tabs.targetFor(tab.id), tab.id))
}
function SideChat({ tabs, sessionId, visible, tabId, onOutput, onKeep }: { tabs: SessionTabs; sessionId: string; visible: boolean; tabId: string; onOutput(id: string, signature: string): void; onKeep(id: string): void }) {
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
  // Each chat owns its files and transcript; it must not inherit Main's viewer destinations.
  return <ViewerHostContext.Provider value={null}><SessionBoardHost tabs={tabs} sessionId={sessionId} />{session ? <AgentChatView onMessageSent={sent} sessionCommands={false} readArtifactLink={visible} session={session} transcript={transcript} thinking={thinking} compact={compact} checklistItems={live.source === 'server' ? live.live.checklist?.items : undefined} mcpHold={live.live.mcpHold ?? null} canReleasePrompt={live.source === 'loading' ? undefined : live.live.canReleasePrompt === true} canAnswerApprovals={live.source === 'loading' ? null : live.live.canReleasePrompt === true} reasoningTokens={live.live.reasoningTokens === true} /> : <p>This workspace is unavailable.</p>}</ViewerHostContext.Provider>
}
