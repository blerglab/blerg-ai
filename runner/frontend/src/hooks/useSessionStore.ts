import { create } from 'zustand'
import type { DaemonInfo, SessionInfo, SessionStatus, InitialState, DaemonConnected, DaemonDisconnected, SessionStarted, SessionStateChanged, SessionReadChanged, SessionStarChanged, SessionMetaChanged, SessionTitleChanged, SessionEnded } from '../types'
import { onMessage } from '../ws'
import { toastForTransition } from '../notify'
import { useToastStore } from './useToastStore'

// Module-level timer handles so they're cancellable without storing in zustand state.
const pendingTimers = new Map<string, ReturnType<typeof setTimeout>>()

const PENDING_TIMEOUT_MS = 90_000
const PENDING_TIMEOUT_MESSAGE = "This is taking longer than expected — it may have failed to start. Try again or check the sidebar."

interface SessionStore {
  daemons: DaemonInfo[]
  sessions: SessionInfo[]
  serverVersion: string
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
}

export const useSessionStore = create<SessionStore>((set, get) => {
  // Register WS handlers once at store creation time
  onMessage<InitialState>('initial_state', (msg) => {
    const now = Date.now()
    const statusChangedAt: Record<string, number> = {}
    for (const s of msg.sessions) {
      statusChangedAt[s.id] = now
    }
    set({ daemons: msg.daemons, sessions: msg.sessions, statusChangedAt, serverVersion: msg.server_version ?? "" })
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

    // Always surface a waiting/idle transition as an in-app toast (with sound)
    // when the page is open — independent of engagement. The server separately
    // decides whether to also send an OS push (only when you're away; see
    // activityTTL). "Always toast, sometimes notify." Only fire on an actual
    // change; toastForTransition decides which states notify.
    // starting → idle is a session becoming ready, not one finishing a turn.
    const becameReady = (prev?.status === 'starting' || prev?.status === 'disconnected') && msg.status === 'idle'
    if (prev && prev.status !== msg.status && !becameReady) {
      const toast = toastForTransition({ ...prev, status: msg.status }, msg.status)
      if (toast) useToastStore.getState().addToast(toast)
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
            }
          : s
      ),
      statusChangedAt: { ...curState.statusChangedAt, [msg.session_id]: Date.now() },
    }))
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

  onMessage<SessionEnded>('session_ended', (msg) => {
    set((state) => ({
      sessions: state.sessions.map(s =>
        s.id === msg.session_id
          ? { ...s, status: 'stopped', end_reason: msg.end_reason ?? s.end_reason, ended_by: msg.ended_by ?? s.ended_by }
          : s
      ),
      statusChangedAt: { ...state.statusChangedAt, [msg.session_id]: Date.now() },
    }))
  })

  return {
    daemons: [],
    sessions: [],
    serverVersion: "",
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
  }
})
