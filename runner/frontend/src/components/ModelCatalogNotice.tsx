import { IconButton } from '@blerglab/chat'
import { coreOrigin } from '../authClient'
import type { EngineModelsState } from '../lib/engineModels'

export function ModelReconnectAction() {
  return <a href={`${coreOrigin()}/settings#managed-connections`} target="_blank" rel="noopener noreferrer">Reconnect in Settings</a>
}

export const CLUSTER_CODEX_CONNECTION_REASON = 'Connect Codex for cluster workspaces'

export function ClusterCodexConnectionNotice() {
  return <div role="alert" data-testid="cluster-codex-connection" style={{
    marginTop: 8, padding: 12, border: '1px solid var(--amber)', borderRadius: 8,
    background: 'color-mix(in srgb, var(--amber) 14%, var(--basalt))',
  }}>
    <strong style={{ display: 'block', marginBottom: 8 }}>{CLUSTER_CODEX_CONNECTION_REASON}</strong>
    <a className="settings-action btn primary" style={{ display: 'inline-block' }} href={`${coreOrigin()}/settings#managed-connections`} target="_blank" rel="noopener noreferrer">Connect Codex</a>
  </div>
}

// Catalog discovery never blocks the current model or a launch with defaults.
export default function ModelCatalogNotice({ state }: { state: EngineModelsState }) {
  if (state.error) return <span role="alert">{state.error} {state.reconnect && <ModelReconnectAction />} {state.retry && <IconButton label="Retry models" onClick={state.retry} />}</span>
  if (state.refreshing && state.models.length === 0) return <span role="status">Models are refreshing; you can keep using the engine default.</span>
  return null
}
