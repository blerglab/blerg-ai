import { useTranscriptStore } from '@blerglab/chat'
import type { SessionInfo } from '../types'
import { launchPermission, latestCapabilitiesEvent, normalizeCapabilities, permissionLabel } from '../lib/capabilities'
import PermissionChip from './PermissionChip'

export default function SessionPermissionChip({ session }: { session: SessionInfo }) {
  const event = useTranscriptStore(s => latestCapabilitiesEvent(s.sessions[session.id]?.events))
  const payload = normalizeCapabilities(event?.payload)
  const permission = payload?.permission_mode
    ? permissionLabel(payload.permission_mode, payload.engine, payload.sandbox_mode)
    : session.kind !== 'tmux'
      ? { label: 'No prompts', risky: true, description: 'No engine permission prompts; access scope not reported yet. Runtime confinement still applies.' }
      : launchPermission(session.engine || 'claude', session.kind || 'tmux', session.skip_permissions)
  return <PermissionChip permission={permission} testId="session-permission" />
}
