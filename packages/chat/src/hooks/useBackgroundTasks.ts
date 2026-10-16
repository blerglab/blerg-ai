// useBackgroundTasks: what a session's agent left running in the background (a background shell, a
// Monitor, a background subagent), read from the events the transcript store holds for it, for a
// view that is not the chat itself — the host's side panel. Running tasks while the session is live;
// a finished one for DONE_LINGER_MS after its end; a failure since this view opened until it is
// dismissed.
import { useMemo, useState } from 'react'
import { create } from 'zustand'
import { backgroundTasks, shownTasks, taskFailed, type BackgroundTask } from '../model/backgroundTasks'
import { useTicker } from './useTicker'
import { useTranscriptStore } from './useTranscriptStore'

interface LiveState {
  /** Whether each session can still be running something, as the ChatView showing it last said. */
  live: Record<string, boolean>
  setLive: (sessionId: string, live: boolean) => void
}

/** Where ChatView says whether its session is live, so a view elsewhere on the page agrees with it. */
export const useBackgroundLiveStore = create<LiveState>(set => ({
  live: {},
  setLive: (sessionId, live) => set(s => (s.live[sessionId] === live ? s : { live: { ...s.live, [sessionId]: live } })),
}))

export interface BackgroundTaskCounts {
  running: number
  /** Finished tasks still shown (just ended, or failed and not dismissed). */
  recent: number
  /** Of those, the ones that failed. */
  failed: number
}

export interface BackgroundTasksHandle {
  /** Tasks still running, oldest first. */
  running: BackgroundTask[]
  /** Finished tasks still shown, oldest first. */
  recent: BackgroundTask[]
  counts: BackgroundTaskCounts
  /** Server-time ms now, ticking each second while anything is shown. */
  now: number
  /** Takes a finished task away (a failure stays until this). */
  dismiss: (taskId: string) => void
}

export interface UseBackgroundTasksOptions {
  /** Whether the session can still be running something. Default: what the ChatView showing the
   *  session says (false when none has). */
  live?: boolean
}

const NO_TASKS: BackgroundTask[] = []

/** The background tasks of a session as a view outside the chat shows them. Its state (what was
 *  dismissed, when it began watching) is the mount's: render it under a key of the session. */
export function useBackgroundTasks(sessionId: string, opts: UseBackgroundTasksOptions = {}): BackgroundTasksHandle {
  const events = useTranscriptStore(s => s.sessions[sessionId]?.events)
  const clockOffset = useTranscriptStore(s => s.sessions[sessionId]?.clockOffset ?? 0)
  const reported = useBackgroundLiveStore(s => s.live[sessionId])
  const live = opts.live ?? reported ?? false
  const tasks = useMemo(() => (events ? backgroundTasks(events) : NO_TASKS), [events])

  // When this view started watching the session (server time): only a failure after it stays
  // until it is dismissed. Both belong to one session: a view that switches sessions is remounted
  // under the session's key, as ChatView is.
  const [openedAt] = useState(() => Date.now() - clockOffset)
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(() => new Set())

  // The clock runs only while something is shown (it decides when a finished task goes).
  const [ticking, setTicking] = useState(false)
  const now = useTicker(ticking) - clockOffset
  const shown = shownTasks(tasks, now, live, openedAt, dismissed)
  const want = shown.length > 0
  if (want !== ticking) setTicking(want)

  const running = shown.filter(t => !t.done)
  const recent = shown.filter(t => t.done)
  return {
    running,
    recent,
    counts: { running: running.length, recent: recent.length, failed: recent.filter(taskFailed).length },
    now,
    dismiss: (taskId: string) => setDismissed(d => (d.has(taskId) ? d : new Set(d).add(taskId))),
  }
}
