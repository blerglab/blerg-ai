// Chat/timeline view for agent-kind sessions: the runner's page over @blerglab/chat's ChatView.
// The package draws the transcript, composer, files and viewer; this file adds what is the
// runner's own — the model and effort pickers, the Rules panel, the ready card's repo line — and
// wires the package to the runner's websocket and token. The chat's settings (Show thinking,
// Compact tool calls), its Files and the Skills & plugins are in the session panel (SessionRail).
import { memo, useEffect, useMemo, useState } from 'react'
import {
  ChatView,
  ReadyCard,
  createBlergTransport,
  type SessionMeta,
  type Transport,
} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import * as ws from '../ws'
import { ensureFreshToken, getAccessToken } from '../authClient'
import { bounceToRefresh } from '../signIn'
import { tokenExpired } from '../claims'
import { apiFetch } from '../apiFetch'
import { effectiveEffort, modelCatalogTarget, useEngineModels } from '../lib/engineModels'
import { ENGINE_LABELS, RUNTIME_LABELS } from '../lib/runtimes'
import { repoDetail } from '../lib/noRepo'
import type { SessionInfo } from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import './AgentChatView.css'
import ModelCatalogNotice from './ModelCatalogNotice'

// The ?artifact=<id> of the address: the link `blerg-runner publish` prints.
function artifactParam(): string | null {
  return new URLSearchParams(window.location.search).get('artifact')
}

// Takes ?artifact= out of the address (history.replaceState: no navigation, no new entry), so a
// reload does not open the viewer again.
function clearArtifactParam() {
  const u = new URL(window.location.href)
  if (!u.searchParams.has('artifact')) return
  u.searchParams.delete('artifact')
  window.history.replaceState(window.history.state, '', u.pathname + u.search + u.hash)
}

// One transport for the page: the runner's shared socket and its browser token. A token that
// looks expired is renewed before a request; a route that still answers 401 sends the page to
// core's refresh, as apiFetch does.
let shared: Transport | null = null
function blergTransport(): Transport {
  if (shared) return shared
  shared = createBlergTransport({
    socket: ws,
    getToken: async () => {
      const tok = getAccessToken()
      if (!tok || tokenExpired(tok)) await ensureFreshToken()
      return getAccessToken()
    },
    onUnauthorized: () => {
      bounceToRefresh()
    },
  })
  return shared
}

interface AgentRule {
  id: string
  project: string
  content: string
  enabled: boolean
}

// RulesPanel: the human approval surface for agent-proposed standing rules.
function RulesPanel({ project }: { project: string }) {
  const [rules, setRules] = useState<AgentRule[]>([])
  const load = () => {
    void apiFetch(`/api/agent/rules?project=${encodeURIComponent(project)}`)
      .then(r => r.json())
      .then((data: AgentRule[]) => setRules(Array.isArray(data) ? data : []))
      .catch(() => {})
  }
  useEffect(load, [project])
  async function setEnabled(id: string, enabled: boolean) {
    await apiFetch(`/api/agent/rules/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    }).catch(() => {})
    load()
  }
  async function remove(id: string) {
    await apiFetch(`/api/agent/rules/${id}`, { method: 'DELETE' }).catch(() => {})
    load()
  }
  return (
    <div className="rules-panel" data-testid="rules-panel">
      {rules.length === 0 && <div className="rules-empty">No rules for {project}. The agent can propose rules with rule_propose; they take effect once you approve them here.</div>}
      {rules.map(r => (
        <div key={r.id} className={`rule-row ${r.enabled ? 'enabled' : 'pending'}`}>
          <span className="rule-status">{r.enabled ? '✓ active' : '⏳ pending'}</span>
          <span className="rule-content">{r.content}</span>
          <button onClick={() => void setEnabled(r.id, !r.enabled)}>
            {r.enabled ? 'Disable' : 'Approve'}
          </button>
          <button className="rule-delete" onClick={() => void remove(r.id)}>✕</button>
        </div>
      ))}
    </div>
  )
}


interface Props {
  session: SessionInfo
  /** Whether the model's thinking is drawn (the panel's Show thinking). */
  thinking: boolean
  /** Whether runs of tool calls fold into one row (the panel's Compact tool calls). */
  compact: boolean
  /** A file the History tab asked to open; n changes with every request. */
  openArtifact?: { id: string; n: number } | null
}

// Memoised: the page around it re-renders as the side panel's state moves, the chat need not.
export default memo(AgentChatView)

function AgentChatView({ session, thinking, compact, openArtifact }: Props) {
  const sessionId = session.id
  const transport = blergTransport()
  // The server's per-file limit (initial_state): the chat refuses a larger attachment before sending it.
  const maxFileBytes = useSessionStore(s => s.maxFileBytes)
  const [showRules, setShowRules] = useState(false)
  const [linked, setLinked] = useState<string | null>(artifactParam)
  // A request from the History tab, taken once: the chat consumes the link when its viewer opens it.
  const [askSeen, setAskSeen] = useState(openArtifact?.n ?? 0)
  if (openArtifact && openArtifact.n !== askSeen) {
    setAskSeen(openArtifact.n)
    setLinked(openArtifact.id)
  }
  // The in-session switcher offers the same list the launch sheet does for
  // this session's engine, and the efforts of the model it is on now. A model
  // the list doesn't know (an alias, an older id) gets every level any model
  // of the engine offers; one that takes no effort (Haiku) gets no effort
  // control at all, and neither does an engine with no model list. A list a
  // daemon reports (Codex, Hermes) is asked of the session's own daemon.
  const catalogTarget = modelCatalogTarget(session.runtime, session.daemon_id)
  const engineModels = useEngineModels(session.engine ?? '', catalogTarget.daemonId, { runtime: catalogTarget.runtime })
  const currentModel = engineModels.models.find(m => m.id === session.model)
  const sessionEfforts: readonly string[] = currentModel
    ? currentModel.efforts
    : Array.from(new Set(engineModels.models.flatMap(m => m.efforts)))

  const meta: SessionMeta = session
  const engine = ENGINE_LABELS[session.engine || 'claude'] ?? session.engine
  const runtime = RUNTIME_LABELS[session.runtime || 'daemon'] ?? session.runtime
  const slots = useMemo(() => ({
    header: (
      <>
        <ModelCatalogNotice state={engineModels} />
        <select
          aria-label="Model"
          value={session.model ?? ''}
          onChange={e => {
            const model = e.target.value
            const effort = effectiveEffort(engineModels.models.find(m => m.id === model), session.effort ?? null)
            ws.send({ type: 'set_session_model', session_id: sessionId, model, ...(effort ? { effort } : {}) })
          }}
        >
          {!session.model && <option value="">model…</option>}
          {/* A session launched on an id or alias the list doesn't have
              (an older id, "sonnet") still shows what it is running. */}
          {session.model && !currentModel && <option value={session.model}>{session.model}</option>}
          {engineModels.models.map(m => (
            <option key={m.id} value={m.id} title={m.description || m.id}>{m.name}</option>
          ))}
        </select>
        {sessionEfforts.length > 0 && (
          <select
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
          </select>
        )}
        <button
          className="agent-rules-toggle"
          data-testid="rules-toggle"
          aria-label="Rules"
          title="Rules"
          onClick={() => setShowRules(s => !s)}
        >
          <span className="tb-icon" aria-hidden="true">⚖</span>
          <span className="tb-label">Rules</span>
        </button>
        {showRules && <RulesPanel project={session.repo} />}
      </>
    ),
    readyCard: (m: SessionMeta, readyAt: number) => (
      <ReadyCard meta={{ ...m, engine, runtime }} readyAt={readyAt}>
        <div><dt>Repo</dt><dd data-testid="ready-repo">{repoDetail(session.repo)}</dd></div>
      </ReadyCard>
    ),
  }), [session.model, session.effort, session.repo, sessionId, currentModel, engineModels, sessionEfforts, showRules, engine, runtime])

  return (
    <ChatView
      session={sessionId}
      transport={transport}
      meta={meta}
      slots={slots}
      thinking={thinking ? 'shown' : 'hidden'}
      compact={compact}
      filesButton={false}
      maxFileBytes={maxFileBytes ?? undefined}
      linkedArtifact={linked}
      onLinkedArtifactConsumed={() => { setLinked(null); clearArtifactParam() }}
    />
  )
}
