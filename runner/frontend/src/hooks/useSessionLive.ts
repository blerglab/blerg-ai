// useSessionLive: what the session page's header badge, auto-open and side panel read about what is
// live in an agent session. From the server (useLiveStateServer) when it has the route: the whole
// log, right on first paint. From the chat's useLiveState (the events the transcript has loaded)
// when it does not, or when it could not answer and has never answered (an older server, a server
// down). Nothing is listed while the first read is on its way: a list that fills in at once beats one
// that churns.
import { useMemo } from 'react'
import { apiFetch } from '../apiFetch'
import { useMessageStore } from './useMessageStore'
import { useChatReportStore, useLiveState, type BackgroundTask, type ChatReport } from '@blerglab/chat'
import type { SessionPanelLiveState, LiveChildSession, LiveDelegationGrant, LiveDelegationRequest, LiveReviewRequest, LiveAnswerRequest } from '../lib/liveState'
import { useLiveStateServer } from './useLiveStateServer'

export interface SessionLive {
  children?: LiveChildSession[]
  delegationRequests?: LiveDelegationRequest[]
  delegationGrants?: LiveDelegationGrant[]
  /** Pending review requests (Needs you). */
  reviewRequests?: LiveReviewRequest[]
  /** Pending published-page answers (Needs you). */
  answerRequests?: LiveAnswerRequest[]
  /** The panel's lists, in the chat's LiveState shape (answer, openFiles, filesKey and files are the chat's). */
  live: SessionPanelLiveState
  /** The background tasks still running, by the server; null when the chat's list is used. */
  tasks: BackgroundTask[] | null
  /** The viewer's pending tool changes on the session, by the server; null when the page must read
   *  them itself (the chat's list is used) or before the first answer. */
  toolChanges: number | null
  /** Where the lists come from. */
  source: 'server' | 'client' | 'loading'
}

const NO_REPORT: Partial<ChatReport> = {}

const NO_CLOSED: ReadonlyMap<string, string> = new Map()

export function useSessionLive(sessionId: string, enabled: boolean, refresh: unknown): SessionLive {
  const messages = useMessageStore(s => s.messages)
  const report = useChatReportStore(s => s.reports[sessionId]) ?? NO_REPORT
  // An ask the server closed without a transcript event (it expired, or was answered on the Messages
  // page) reaches the chat as message_answered: it leaves the lists at once, and the server is asked
  // again.
  const closed = report.closedAsks ?? NO_CLOSED
  const server = useLiveStateServer(sessionId, enabled, `${String(refresh)}|${closed.size}|${messages.filter(m => m.kind === 'ask').map(m => `${m.id}:${m.status}:${m.answerable}`).join(',')}`)
  const served = server.status === 'ready' || server.status === 'error' ? server.view : null
  const view = useMemo(() => (served && closed.size > 0 && served.asks.some(a => a.messageId && closed.has(a.messageId))
    ? { ...served, asks: served.asks.filter(a => !(a.messageId && closed.has(a.messageId))) }
    : served), [served, closed])
  const reply = server.status === 'ready' || server.status === 'error' ? server.reply : null
  const fallback = !view && (server.status === 'no-route' || server.status === 'error' || !enabled)
  // The chat's own reading, only when it is the one shown (it rebuilds on every event).
  const client = useLiveState(fallback ? sessionId : '')
  const clockOffset = server.status === 'ready' || server.status === 'error' ? server.clockOffset : 0
  const generatedAt = reply ? Date.parse(reply.generated_at) : NaN
  const parentAnswer = report.answer
  const fromServer = useMemo<SessionPanelLiveState | null>(() => (view
    ? {
        settled: true,
        subagents: view.subagents,
        openAsks: view.asks.filter(a => !messages.some(m => m.id === a.messageId && m.status !== 'open')),
        backgroundRunning: view.tasks.length,
        answer: parentAnswer || view.asks.some(a => a.childSessionId) ? async (text, messageId) => {
          const child = view.asks.find(a => a.messageId === messageId && a.childSessionId)
          if (!child) return await parentAnswer?.(text, messageId) ?? false
          try {
            const response = await apiFetch(`/api/messages/${encodeURIComponent(child.messageId)}/reply`, {
              method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ answer: text }),
            })
            if (!response.ok) return false
            const answered = { id: child.messageId, session_id: child.childSessionId!, kind: 'ask' as const,
              body: child.question, status: 'answered' as const, answer: text,
              created_at: new Date(child.askedAt ?? Date.now()).toISOString(), answered_at: new Date().toISOString() }
            useMessageStore.setState(s => ({ messages: s.messages.some(m => m.id === answered.id)
              ? s.messages.map(m => m.id === answered.id ? answered : m) : [...s.messages, answered] }))
            return true
          } catch { return false }
        } : undefined,
        openFiles: report.openFiles,
        filesKey: report.filesKey ?? '',
        files: report.files,
        clockOffset,
        // What "new since" is measured against: the reply's time (an ask folded in after it is new).
        heldUntil: Number.isFinite(generatedAt) ? generatedAt : null,
        checklist: view.checklist,
        plan: view.plan,
        progress: view.progress,
        tickets: view.tickets,
        ticket: view.ticket,
        mcp: view.mcp,
        mcpHold: view.mcpHold,
        canReleasePrompt: view.canReleasePrompt,
        reasoningTokens: view.reasoningTokens,
        usage: view.usage,
      }
    : null), [view, messages, parentAnswer, report.openFiles, report.filesKey, report.files, clockOffset, generatedAt])
  if (fromServer && view) return { live: fromServer, tasks: view.tasks, toolChanges: view.toolChanges, children: view.children, delegationRequests: view.delegationRequests, delegationGrants: view.delegationGrants, reviewRequests: view.reviewRequests, answerRequests: view.answerRequests, source: 'server' }
  if (fallback) return { live: client, tasks: null, toolChanges: null, source: 'client' }
  // Loading: the chat's reading of no session, which lists nothing.
  return { live: client, tasks: [], toolChanges: null, source: 'loading' }
}
