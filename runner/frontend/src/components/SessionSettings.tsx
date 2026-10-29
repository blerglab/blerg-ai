import './SessionSettings.css'
import { useMemo } from 'react'
import { modelDisplayName, modelDisplayNames } from '@blerglab/chat'
import { usePaletteCommands } from '../commands/context'
import { effectiveEffort, modelCatalogTarget, useEngineModels } from '../lib/engineModels'
import * as ws from '../ws'
import type { SessionInfo } from '../types'
import ModelCatalogNotice from './ModelCatalogNotice'
import { launchModelsCache } from '../lib/launchCache'

export default function SessionSettings({ session, commandsOnly = false }: { session: SessionInfo; commandsOnly?: boolean }) {
  const sessionId = session.id
  // The in-session switcher offers the same list the launch sheet does for
  // this session's engine, and the efforts of the model it is on now. A model
  // the list doesn't know (an alias, an older id) gets every level any model
  // of the engine offers; one that takes no effort (Haiku) gets no effort
  // control at all, and neither does an engine with no model list. A list a
  // daemon reports (Codex, Hermes) is asked of the session's own daemon.
  const catalogTarget = modelCatalogTarget(session.runtime, session.daemon_id)
  // The launch sheet's model lists (per account, engine and daemon; names only) fill the menu at
  // once on a session switch, and each answer is put back (#250).
  const engineModels = useEngineModels(session.engine ?? '', catalogTarget.daemonId, { runtime: catalogTarget.runtime, cache: launchModelsCache })
  const modelNames = useMemo(() => modelDisplayNames(session.model && !engineModels.models.some(m => m.id === session.model) ? [...engineModels.models, { id: session.model }] : engineModels.models), [engineModels.models, session.model])
  const currentModel = engineModels.models.find(m => m.id === session.model)
  const sessionEfforts: readonly string[] = currentModel
    ? currentModel.efforts
    : Array.from(new Set(engineModels.models.flatMap(m => m.efforts)))

  const paletteCommands = useMemo(() => session.archived_at ? [] : [
    // The empty palette offers one entry each; choosing it lists the choices.
    ...(engineModels.models.some(model => model.id !== session.model) ? [{ id: `model-menu:${sessionId}`, destructive: false, title: 'Switch model…', refine: 'Switch model: ', here: 3, run: () => {} }] : []),
    ...(sessionEfforts.some(effort => effort !== session.effort) ? [{ id: `effort-menu:${sessionId}`, destructive: false, title: 'Set effort…', refine: 'Set effort: ', here: 4, run: () => {} }] : []),
    ...engineModels.models.filter(model => model.id !== session.model).map(model => ({
      id: `model:${sessionId}:${model.id}`, destructive: false, title: `Switch model: ${modelNames.get(model.id) ?? model.id}`,
      run: () => {
        const effort = effectiveEffort(model, session.effort ?? null)
        ws.send({ type: 'set_session_model', session_id: sessionId, model: model.id, ...(effort ? { effort } : {}) })
      },
    })),
    ...sessionEfforts.filter(effort => effort !== session.effort).map(effort => ({ id: `effort:${sessionId}:${effort}`, destructive: false, title: `Set effort: ${effort}`, run: () => ws.send({ type: 'set_session_model', session_id: sessionId, effort }) })),
  ], [session.archived_at, session.effort, session.model, engineModels.models, modelNames, sessionId, sessionEfforts])
  usePaletteCommands(commandsOnly ? paletteCommands : [])

  if (commandsOnly) return null
  // The engine is a static fact (SessionFacts, above); this is only what can change: model and effort.
  if (session.archived_at) return (session.model || session.effort) ? <div className="session-engine-line session-engine-summary" data-testid="session-model-line">
    {session.model && <span title={session.model}>{modelDisplayName(session.model)}</span>}{session.model && session.effort && <span aria-hidden="true">·</span>}{session.effort && <span>{session.effort}</span>}
  </div> : null
  return <div className="rail-settings session-settings"><>
        <ModelCatalogNotice state={engineModels} />
        <div className="session-engine-line" data-testid="session-model-line">
        <label className="session-model"><span className="session-control-label">Model</span><select
          aria-label="Model"
          value={session.model ?? ''}
          title={session.model}
          onChange={e => {
            const model = e.target.value
            const effort = effectiveEffort(engineModels.models.find(m => m.id === model), session.effort ?? null)
            ws.send({ type: 'set_session_model', session_id: sessionId, model, ...(effort ? { effort } : {}) })
          }}
        >
          {!session.model && <option value="">model…</option>}
          {/* A session launched on an id or alias the list doesn't have
              (an older id, "sonnet") still shows what it is running. */}
          {session.model && !currentModel && <option value={session.model} title={session.model}>{modelNames.get(session.model)}</option>}
          {engineModels.models.map(m => (
            <option key={m.id} value={m.id} title={[m.id, m.description].filter(Boolean).join(' — ')}>{modelNames.get(m.id)}</option>
          ))}
        </select></label>
        {sessionEfforts.length > 0 && (
          <><span aria-hidden="true">·</span><label className="session-effort"><span className="session-control-label">Effort</span><select
            aria-label="Effort"
            value={session.effort ?? ''}
            onChange={e => ws.send({ type: 'set_session_model', session_id: sessionId, effort: e.target.value })}
          >
            {!session.effort && <option value="">effort…</option>}
            {session.effort && !sessionEfforts.includes(session.effort) && (
              <option value={session.effort}>{session.effort}</option>
            )}
            {sessionEfforts.map(m => (
              <option key={m} value={m}>{m}</option>
            ))}
          </select></label></>
        )}
        </div>
      </></div>
}
