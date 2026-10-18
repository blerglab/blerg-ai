import type { ProposalInfo } from '../types'
export type PluginScope = 'session' | 'always'
export interface SessionPluginSettings {
  engine: string
  supported: boolean
  session_apply: boolean
  reason?: string
  allowed_marketplaces: string[]
  any_marketplace: boolean
  plugins: { marketplace: string; plugin: string }[]
  requests: ProposalInfo[]
}
export function pluginApplication(p: ProposalInfo): string {
  const result = p.result as { state?: unknown; message?: unknown } | null
  if (typeof result?.message === 'string' && result.message) return result.message
  switch (result?.state) {
    case 'queued':
      return 'Queued until foreground and background work are idle.'
    case 'installing':
      return 'Installing plugin.'
    case 'applied':
      return 'Loaded in this session.'
    case 'new_sessions':
      return 'Saved for new sessions; this session is unchanged.'
  }
  return p.state === 'executing' ? 'Queued or installing; the current turn can finish.' : p.state
}

// Approval can be complete while the daemon still waits for an idle engine.
export function pluginState(p: ProposalInfo): string {
  if (p.kind !== 'plugin_request') return p.state
  const state = (p.result as { state?: unknown } | null)?.state
  return (p.state === 'done' || p.state === 'executing') &&
    typeof state === 'string' &&
    ['queued', 'installing', 'applied', 'new_sessions', 'failed'].includes(state)
    ? state
    : p.state
}
