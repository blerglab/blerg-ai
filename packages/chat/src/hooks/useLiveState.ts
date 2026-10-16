// useLiveState: what is live in a session right now — the subagents still running and the agent's
// questions still open — for a view beside the chat, read from the events the transcript store holds
// and from what the session's ChatView reports. It updates with the transcript and moves by the
// minute (a subagent with no end for STALE_RUNNING_MS stops counting): a view that shows elapsed times
// keeps its own clock.
//
// It sees only what the transcript has loaded, so on a long session its lists fill in as the history
// loads and miss what started before it. The runner's side panel reads the server's whole-log answer
// (GET /api/sessions/{id}/live) and uses this hook only as the fallback for an older runner server; the
// board and other hosts without that route keep using it as their reading.
import { useMemo } from 'react'
import { create } from 'zustand'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { ArtifactsStatus } from './useArtifacts'
import { backgroundTasks, runningTasks } from '../model/backgroundTasks'
import { liveSubagents, openAsks, type AskLive, type LiveReport, type SubagentLive } from '../model/liveState'
import { parseTs } from '../model/timeLabel'
import { sharedTimeline } from '../model/toolGroups'
import { useMinuteClock } from './useMinuteClock'
import { useTranscriptStore } from './useTranscriptStore'

/** A session's Files as its chat holds them, for a view beside the chat that lists them (the
 *  runner's session panel has a Files tab): the chat's own list, so a delete there refreshes the
 *  chat's cards and count too. */
export interface ChatFiles {
  /** The transport's file operations (download, delete). */
  ops: TransportFiles
  /** Newest first, every version (FilesList groups them). */
  items: ArtifactInfo[]
  status: ArtifactsStatus
  /** Reads the list again. */
  refresh: () => void
  /** Opens the message box's file picker: what is picked is attached to the next message. Absent
   *  when the chat has no message box. */
  attach?: () => void
}

/** What a ChatView showing a session reports about it, and what it lets a view beside it do. */
export interface ChatReport extends LiveReport {
  /** Answers an ask (messageId: its messages API id, when it has one); false when nothing was sent.
   *  Absent when the chat cannot address an answer or cannot send now (the ask card has no reply
   *  box then either). */
  answer?: (text: string, messageId?: string) => Promise<boolean>
  /** Opens the chat's Files panel; absent when the chat has none. */
  openFiles?: () => void
  /** The Files list as the chat last read it (its ids, joined by ","): a view beside the chat reads
   *  it again when this changes (a deletion leaves no transcript event). */
  filesKey?: string
  /** The Files list and what can be done with it; absent when the chat has no file operations. */
  files?: ChatFiles
}

interface ReportState {
  reports: Record<string, ChatReport>
  setReport: (sessionId: string, report: ChatReport) => void
  /** The chat that reported on the session is gone: its actions must not be called any more. */
  clearReport: (sessionId: string) => void
}

/** Where each ChatView reports on its session, so a view elsewhere on the page agrees with it. */
export const useChatReportStore = create<ReportState>(set => ({
  reports: {},
  setReport: (sessionId, report) => set(s => ({ reports: { ...s.reports, [sessionId]: report } })),
  clearReport: sessionId => set(s => {
    if (!(sessionId in s.reports)) return s
    const reports = { ...s.reports }
    delete reports[sessionId]
    return { reports }
  }),
}))

const NO_CLOSED: ReadonlyMap<string, string> = new Map()
const NO_REPORT: ChatReport = { inFlight: false, live: false, ended: false, closedAsks: NO_CLOSED }

export interface LiveState {
  /** The session's initial history is loaded (the transcript's replay is complete). The lists
   *  below are read from whatever is held either way; a view may use this to tell a partial
   *  picture from a whole one. False before a chat has shown the session, and again when the
   *  transcript is loaded afresh. */
  settled: boolean
  /** Top-level subagents still running, oldest first. */
  subagents: SubagentLive[]
  /** The agent's questions still open, oldest first. */
  openAsks: AskLive[]
  /** How many background tasks are running (a session that is not live runs none), a background
   *  subagent's task left out: it is counted among the subagents. */
  backgroundRunning: number
  /** Answers an ask; absent when the session's chat cannot send an answer now. */
  answer?: (text: string, messageId?: string) => Promise<boolean>
  /** Opens the session's Files panel, when its chat has one. */
  openFiles?: () => void
  /** ChatReport.filesKey ('' before a chat reports). */
  filesKey: string
  /** ChatReport.files: the session's Files, when its chat has them. */
  files?: ChatFiles
  /** client clock − server clock (ms): the times above are the server's. */
  clockOffset: number
  /** Server-time ms of the newest event held, or null while none is: what "new since" is measured
   *  against without reading a clock. */
  heldUntil: number | null
}

const NO_EVENTS: never[] = []

/** The live state of a session, as the transcript store and its ChatView know it. Before any
 *  ChatView has shown the session, nothing is running and no ask can be answered. */
export function useLiveState(sessionId: string): LiveState {
  const events = useTranscriptStore(s => s.sessions[sessionId]?.events) ?? NO_EVENTS
  // The same test as the chat's skeleton: its replay is done and no forward page remains.
  const settled = useTranscriptStore(s => { const t = s.sessions[sessionId]; return !!t && t.replayDone && !t.hasMore })
  const clockOffset = useTranscriptStore(s => s.sessions[sessionId]?.clockOffset ?? 0)
  const report = useChatReportStore(s => s.reports[sessionId]) ?? NO_REPORT
  const { inFlight, live, ended, closedAsks, answer, openFiles, compact = true, filesKey = '', files } = report
  // The timeline the session's transcript draws (shared: built once for both).
  // The chat's clock in server time, moving by the minute: what has had no end for too long is not running.
  const now = useMinuteClock() - clockOffset
  const subagents = useMemo(
    () => liveSubagents(events, { inFlight, live }, now, evs => sharedTimeline(sessionId, evs, { groupTools: compact })),
    [events, inFlight, live, sessionId, compact, now],
  )
  const asks = useMemo(() => openAsks(events, { ended, closedAsks }), [events, ended, closedAsks])
  // A background subagent is also a background task: counted once, as a subagent.
  const backgroundRunning = useMemo(() => {
    const keys = new Set(subagents.map(sub => sub.key))
    return runningTasks(backgroundTasks(events), live, now).filter(t => !(t.kind === 'agent' && keys.has(t.callId))).length
  }, [events, live, subagents, now])
  const last = events[events.length - 1]
  const heldUntil = last ? parseTs(last.ts) : null
  return { settled, subagents, openAsks: asks, backgroundRunning, answer, openFiles, filesKey, files, clockOffset, heldUntil }
}
