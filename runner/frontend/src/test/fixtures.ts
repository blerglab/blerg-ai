// Shared fixtures for component tests.
import type { DaemonInfo, SessionInfo, SessionStatus } from '../types'

export function makeDaemon(over: Partial<DaemonInfo> = {}): DaemonInfo {
  return {
    id: 'd1',
    name: 'workstation',
    mode: 'local',
    version: '1.0.0',
    repos_root: '/home/dev/repos',
    status: 'connected',
    last_seen_at: '2026-06-13T00:00:00Z',
    ...over,
  }
}

export function makeSession(over: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 'sess-a',
    daemon_id: 'd1',
    status: 'running' as SessionStatus,
    project_path: '/home/dev/repos/widget',
    repo: 'widget',
    title: 'Build the widget',
    started_at: '2026-06-13T00:00:00Z',
    ...over,
  }
}
