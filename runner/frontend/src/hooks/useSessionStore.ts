import { create } from 'zustand'
import type { DaemonInfo, SessionInfo, SessionStatus, InitialState, DaemonConnected, DaemonDisconnected, SessionStarted, SessionStateChanged, SessionReadChanged, SessionStarChanged, SessionMetaChanged, SessionTitleChanged, SessionEnded, SessionArchived, SessionBackgroundChanged, SessionPausedChanged, SessionNotice, DaemonLimitsChanged } from '../types'
import { onMessage } from '../ws'
import { useToastStore } from './useToastStore'

// Module-level timer handles so they're cancellable without storing in zustand state.
const pendingTimers = new Map<string, ReturnType<typeof setTimeout>>()

// Statuses after which a session runs nothing (it ended, failed or lost its process).
const TERMINAL_STATUSES = new Set<SessionStatus>(['stopped', 'ended', 'error', 'disconnected'])

const PENDING_TIMEOUT_MS = 90_000
const PENDING_TIMEOUT_MESSAGE = "This is taking longer than expected — it may have failed to start. Try again or check the sidebar."

interface SessionStore {
  daemons: DaemonInfo[]
  sessions: SessionInfo[]
  serverVersion: string
  /** The release version (the VERSION file) and git commit the server was built from; '' when unstamped. */
  serverRelease: string
  serverCommit: string
  latestVersion: string
  latestUrl: string
  /** The largest file, in bytes, a person may attach: the server's advertised limit; null when it
   *  advertises none (older than 0.2.1), and the chat then assumes its default. */
  maxFileBytes: number | null
  /** The server version this page was loaded from (the first one it was told). */
  loadedVersion: string
  /** The server has since reported a different version: this tab runs an old copy of the app. */
  updated: boolean
  // Tracks when each session last changed status (ms). Set on state_changed and
  // initial load. Used by the UI to show time-in-current-state.
  statusChangedAt: Record<string, number>
  // Tracks sessions that have been requested but haven't arrived via session_started yet.
  pendingSessionIds: Record<string, true>
  // Tracks sessions whose spawn failed (id → error message).
  failedSessions: Record<string, string>
  setInitialState: (daemons: DaemonInfo[], sessions: SessionInfo[]) => void
  upsertDaemon: (daemon: DaemonInfo) => void
  removeDaemon: (daemonId: string, affectedSessionIds: string[]) => void
  addSession: (session: SessionInfo) => void
  updateSessionStatus: (sessionId: string, status: SessionStatus, message?: string | null) => void
  endSession: (sessionId: string, exitCode: number) => void
  addPendingSession: (id: string) => void
  removePendingSession: (id: string) => void
  clearFailedSession: (id: string) => void
  /** Forget that a session is paused (a message was just sent to resume it). */
  clearPaused: (sessionId: string) => void
}

export const useSessionStore = create<SessionStore>((set, get) => {
  // Register WS handlers once at store creation time
  onMessage<InitialState>('initial_state', (msg) => {
    const now = Date.now()
    const statusChangedAt: Record<string, number> = {}
    for (const s of msg.sessions) {
      statusChangedAt[s.id] = now
    }
    const serverVersion = msg.server_version ?? ""
    // The page was loaded from one version of the server; a reconnect that reports another
    // means the server was updated under it, and this tab is still running the old app.
    // The key is the version and the commit: a desktop rebuild keeps the version "dev" and changes
    // only the commit. A server that reports no version is never taken for an update.
    const key = serverVersion === "" ? "" : `${serverVersion}@${msg.server_commit ?? ""}`
    const loaded = get().loadedVersion || key
    set({
      daemons: msg.daemons, sessions: msg.sessions, statusChangedAt, serverVersion,
      serverRelease: msg.server_release ?? "", serverCommit: msg.server_commit ?? "",
      latestVersion: msg.latest_version ?? "", latestUrl: msg.latest_url ?? "",
      maxFileBytes: typeof msg.limits?.max_file_bytes === "number" && msg.limits.max_file_bytes > 0 ? msg.limits.max_file_bytes : null,
      loadedVersion: loaded,
      updated: loaded !== "" && key !== "" && key !== loaded,
    })
  })

  onMessage<DaemonConnected>('daemon_connected', (msg) => {
    set((state) => {
      const existing = state.daemons.findIndex(d => d.id === msg.daemon.id)
      if (existing >= 0) {
        const daemons = [...state.daemons]
        daemons[existing] = msg.daemon
        return { daemons }
      }
      return { daemons: [...state.daemons, msg.daemon] }
    })
  })

  // A daemon's usage limits changed: its full set, replacing what it said before.
  onMessage<DaemonLimitsChanged>('daemon_limits_changed', (msg) => {
    set((state) => {
      if (!state.daemons.some(d => d.id === msg.daemon_id)) return {}
      return { daemons: state.daemons.map(d => d.id === msg.daemon_id ? { ...d, usage_limits: msg.usage_limits ?? [] } : d) }
    })
  })

  onMessage<DaemonDisconnected>('daemon_disconnected', (msg) => {
    set((state) => ({
      daemons: state.daemons.filter(d => d.id !== msg.daemon_id),
      sessions: state.sessions.map(s =>
        msg.affected_session_ids.includes(s.id)
          ? { ...s, status: 'error' }
          : s
      ),
    }))
  })

  onMessage<SessionStarted>('session_started', (msg) => {
    // Cancel the pending fallback timer (idempotent — safe if never added)
    const handle = pendingTimers.get(msg.session.id)
    if (handle !== undefined) {
      clearTimeout(handle)
      pendingTimers.delete(msg.session.id)
    }
    set((state) => {
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { [msg.session.id]: _removed, ...pendingRest } = state.pendingSessionIds
      // Upsert: the server announces a session the moment its row exists and
      // the daemon/pod announces it again when it actually starts (and a
      // resumed cluster session announces itself once more). A late
      // "starting" never overrides a status the session has already moved
      // past to a live one.
      const idx = state.sessions.findIndex(s => s.id === msg.session.id)
      if (idx >= 0) {
        const prev = state.sessions[idx]
        const liveAlready = prev.status === 'running' || prev.status === 'idle' || prev.status === 'waiting'
        const merged: SessionInfo = {
          ...prev,
          ...msg.session,
          status: liveAlready && msg.session.status === 'starting' ? prev.status : msg.session.status,
          starred: prev.starred,
          unread: prev.unread,
        }
        const sessions = [...state.sessions]
        sessions[idx] = merged
        return {
          sessions,
          statusChangedAt: merged.status === prev.status
            ? state.statusChangedAt
            : { ...state.statusChangedAt, [msg.session.id]: Date.now() },
          pendingSessionIds: pendingRest,
        }
      }
      return {
        sessions: [...state.sessions, msg.session],
        statusChangedAt: { ...state.statusChangedAt, [msg.session.id]: Date.now() },
        pendingSessionIds: pendingRest,
      }
    })
  })

  onMessage<SessionStateChanged>('session_state_changed', (msg) => {
    const state = get()
    const prev = state.sessions.find(s => s.id === msg.session_id)

    // If this is an error for a session that is pending but hasn't appeared in
    // sessions yet, record it as a failed spawn and clear the pending entry.
    if (msg.status === 'error' && !prev && state.pendingSessionIds[msg.session_id]) {
      const handle = pendingTimers.get(msg.session_id)
      if (handle !== undefined) {
        clearTimeout(handle)
        pendingTimers.delete(msg.session_id)
      }
      set((curState) => {
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { [msg.session_id]: _removed, ...pendingRest } = curState.pendingSessionIds
        return {
          pendingSessionIds: pendingRest,
          failedSessions: { ...curState.failedSessions, [msg.session_id]: msg.message ?? '' },
        }
      })
      return
    }

    set((curState) => ({
      sessions: curState.sessions.map(s =>
        s.id === msg.session_id
          ? {
              ...s,
              status: msg.status,
              message: msg.message,
              unread: msg.unread,
              // The reason travels with the error broadcast; keep it on the
              // session so views don't wait for a reload to explain it.
              error_reason: msg.status === 'error' ? (msg.message || s.error_reason) : s.error_reason,
              // A terminal broadcast carries why it ended; any other status
              // means the session is going (again), so nothing ended it.
              end_reason: msg.end_reason,
              ended_by: msg.ended_by ?? null,
              // A session that ended or failed runs nothing in the background any more.
              background_tasks: TERMINAL_STATUSES.has(msg.status) ? 0 : s.background_tasks,
              // Paused by a restart until it runs again or ends (the server clears it the same way).
              paused_reason: msg.status === 'idle' || msg.status === 'waiting' ? s.paused_reason : undefined,
            }
          : s
      ),
      statusChangedAt: { ...curState.statusChangedAt, [msg.session_id]: Date.now() },
    }))
  })

  // The server's notice that a session needs the person (waiting for input, or idle with nothing
  // left running): always an in-app toast (with sound) while the page is open, whatever the
  // engagement. The server decides when one is due — never for a session nobody attends, and for
  // an idle one only once its background work is done — and separately whether to also send an OS
  // push (only when you're away). "Always toast, sometimes notify."
  onMessage<SessionNotice>('session_notice', (msg) => {
    useToastStore.getState().addToast({ title: msg.title, body: msg.body, url: msg.url })
  })

  onMessage<SessionReadChanged>('session_read_changed', (msg) => {
    set((curState) => ({
      sessions: curState.sessions.map(s =>
        s.id === msg.session_id ? { ...s, unread: false } : s
      ),
    }))
  })

  onMessage<SessionStarChanged>('session_star_changed', (msg) => {
    set((curState) => ({
      sessions: curState.sessions.map(s =>
        s.id === msg.session_id ? { ...s, starred: msg.starred } : s
      ),
    }))
  })

  onMessage<SessionMetaChanged>('session_meta_changed', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s => {
        if (s.id !== msg.session_id) return s
        return {
          ...s,
          model: msg.model || s.model,
          effort: msg.effort || s.effort,
        }
      }),
    }))
  })

  // A background task of a session started or ended: the sidebar's "⏱ N" chip follows the count the
  // server keeps, so it is right for a session nobody has open.
  onMessage<SessionBackgroundChanged>('session_background_changed', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s =>
        s.id === msg.session_id ? { ...s, background_tasks: msg.background_tasks } : s
      ),
    }))
  })

  // A session's daemon restarted and left it paused, waiting for a message (or it runs again).
  onMessage<SessionPausedChanged>('session_paused_changed', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s =>
        s.id === msg.session_id ? { ...s, paused_reason: msg.paused_reason || undefined } : s
      ),
    }))
  })

  onMessage<SessionTitleChanged>('session_title_changed', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s => {
        if (s.id !== msg.session_id) return s
        return {
          ...s,
          title: msg.title,
        }
      }),
    }))
  })

  onMessage<SessionArchived>('session_archived', msg => {
    set(state => ({sessions: state.sessions.map(session => session.id === msg.session_id ? {...session, archived_at: msg.archived_at} : session)}))
  })

  onMessage<SessionEnded>('session_ended', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s =>
        s.id === msg.session_id
          ? { ...s, status: 'stopped', end_reason: msg.end_reason ?? s.end_reason, ended_by: msg.ended_by ?? s.ended_by, background_tasks: 0 }
          : s
      ),
      statusChangedAt: { ...state.statusChangedAt, [msg.session_id]: Date.now() },
    }))
  })

  return {
    daemons: [],
    sessions: [],
    serverVersion: "",
    serverRelease: "",
    serverCommit: "",
    latestVersion: "",
    latestUrl: "",
    maxFileBytes: null,
    loadedVersion: "",
    updated: false,
    statusChangedAt: {},
    pendingSessionIds: {},
    failedSessions: {},

    setInitialState: (daemons, sessions) => set({ daemons, sessions }),

    upsertDaemon: (daemon) =>
      set((state) => {
        const existing = state.daemons.findIndex(d => d.id === daemon.id)
        if (existing >= 0) {
          const daemons = [...state.daemons]
          daemons[existing] = daemon
          return { daemons }
        }
        return { daemons: [...state.daemons, daemon] }
      }),

    removeDaemon: (daemonId, affectedSessionIds) =>
      set((state) => ({
        daemons: state.daemons.filter(d => d.id !== daemonId),
        sessions: state.sessions.map(s =>
          affectedSessionIds.includes(s.id) ? { ...s, status: 'error' } : s
        ),
      })),

    addSession: (session) =>
      set((state) => ({ sessions: [...state.sessions, session] })),

    updateSessionStatus: (sessionId, status, message) =>
      set((state) => ({
        sessions: state.sessions.map(s =>
          s.id === sessionId ? { ...s, status, message: message ?? null } : s
        ),
      })),

    endSession: (sessionId) =>
      set((state) => ({
        sessions: state.sessions.map(s =>
          s.id === sessionId ? { ...s, status: 'stopped' } : s
        ),
      })),

    addPendingSession: (id) => {
      set((state) => ({
        pendingSessionIds: { ...state.pendingSessionIds, [id]: true },
      }))
      // Cancel any pre-existing timer for this id before starting a new one.
      const existing = pendingTimers.get(id)
      if (existing !== undefined) clearTimeout(existing)
      // Start the 90s fallback: if session_started never arrives, record failure.
      const handle = setTimeout(() => {
        pendingTimers.delete(id)
        set((state) => {
          // Guard: if the id was already removed from pending (e.g. by arrival
          // or removePendingSession), treat this as a no-op.
          if (!state.pendingSessionIds[id]) return {}
          // eslint-disable-next-line @typescript-eslint/no-unused-vars
          const { [id]: _removed, ...pendingRest } = state.pendingSessionIds
          return {
            pendingSessionIds: pendingRest,
            failedSessions: { ...state.failedSessions, [id]: PENDING_TIMEOUT_MESSAGE },
          }
        })
      }, PENDING_TIMEOUT_MS)
      pendingTimers.set(id, handle)
    },

    removePendingSession: (id) => {
      const handle = pendingTimers.get(id)
      if (handle !== undefined) {
        clearTimeout(handle)
        pendingTimers.delete(id)
      }
      set((state) => {
        if (!state.pendingSessionIds[id]) return {}
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { [id]: _removed, ...pendingRest } = state.pendingSessionIds
        return { pendingSessionIds: pendingRest }
      })
    },

    clearFailedSession: (id) => {
      set((state) => {
        if (!(id in state.failedSessions)) return {}
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { [id]: _removed, ...rest } = state.failedSessions
        return { failedSessions: rest }
      })
    },

    clearPaused: (sessionId) => {
      set((state) => ({
        sessions: state.sessions.map(s =>
          s.id === sessionId && s.paused_reason ? { ...s, paused_reason: undefined } : s
        ),
      }))
    },
  }
})
