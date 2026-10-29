import { coreOrigin } from '../authClient'
import type { EngineModelsState } from '../lib/engineModels'

export function ModelReconnectAction() {
  return <a href={`${coreOrigin()}/settings#managed-connections`} target="_blank" rel="noopener noreferrer">Reconnect in Settings</a>
}

// Catalog discovery never blocks the current model or a launch with defaults.
export default function ModelCatalogNotice({ state }: { state: EngineModelsState }) {
  if (state.error) return <span role="alert">{state.error} {state.reconnect && <ModelReconnectAction />} {state.retry && <button type="button" className="btn small" onClick={state.retry}>Retry models</button>}</span>
  if (state.refreshing && state.models.length === 0) return <span role="status">Models are refreshing; you can keep using the engine default.</span>
  return null
}
