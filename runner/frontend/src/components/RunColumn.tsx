import type { ReactNode, CSSProperties } from 'react'
import {
  RUNTIME_CARDS,
  ENGINE_LABELS,
  AGENT_NO_PROMPTS,
  runtimeDisabledReason,
} from '../lib/runtimes'
import type { RunDaemon, RunRuntime, RunKind, RunEngine } from '../lib/runtimes'
import type { EngineModel } from '../types'
import ModelPicker, { type ModelPickerFreeText } from './ModelPicker'
import ReposRootEditor from './ReposRootEditor'

// RunColumn is the launch sheet's third column: every "where and how does this
// session run" choice, visible at once. Nothing here is collapsed — the
// runtime is a security-relevant decision, and a collapsed <details> made the
// unsandboxed host the quiet default (spec §1).

// ─── Styles ──────────────────────────────────────────────────────────────────

const sectionLabel: CSSProperties = {
  color: 'var(--fog)',
  fontSize: '0.7rem',
  fontWeight: 700,
  letterSpacing: '0.1em',
  textTransform: 'uppercase',
  marginBottom: 8,
  display: 'block',
}

const noteStyle: CSSProperties = {
  fontSize: '0.72rem',
  color: 'var(--fog-dim)',
  margin: '6px 0 0',
}

const warnStyle: CSSProperties = {
  fontSize: '0.72rem',
  color: 'var(--amber)',
  background: 'color-mix(in srgb, var(--amber) 14%, var(--basalt))',
  border: '1px solid var(--amber)',
  borderRadius: 6,
  padding: '6px 10px',
  margin: '8px 0 0',
}

function pillStyle(selected: boolean, dimmed = false): CSSProperties {
  return {
    background: selected ? 'var(--selected-bg)' : 'var(--scree)',
    border: `1px ${dimmed ? 'dashed' : 'solid'} ${selected ? 'var(--selected-border)' : dimmed ? 'var(--fog-dim)' : 'var(--stone)'}`,
    borderRadius: 6,
    color: selected ? 'var(--selected-text)' : dimmed ? 'var(--fog-dim)' : 'var(--fog)',
    opacity: dimmed ? 0.6 : 1,
    padding: '5px 14px',
    cursor: 'pointer',
    fontSize: '0.85rem',
    fontFamily: 'inherit',
    fontWeight: selected ? 700 : 400,
    letterSpacing: '0.03em',
  }
}

// ─── Props ───────────────────────────────────────────────────────────────────

export interface RunColumnProps {
  runtime: RunRuntime
  onRuntimeChange: (rt: RunRuntime) => void
  kind: RunKind
  onKindChange: (k: RunKind) => void
  engine: RunEngine
  onEngineChange: (e: RunEngine) => void
  // Model picker for the chosen engine: its list from GET /api/models/{engine}
  // (or its built-in fallback; [] = no picker), the selected model's id, and
  // its effort (undefined when the model takes none).
  models: readonly EngineModel[]
  modelsLoading?: boolean
  modelsFallback?: boolean
  modelsError?: string
  modelsRefreshing?: boolean
  onModelsRetry?: () => void
  model: string
  onModelChange: (id: string) => void
  effort?: string
  onEffortChange?: (e: string) => void
  // Free-text model name, offered by the picker when no list was reported.
  modelFreeText?: ModelPickerFreeText
  // The person's model or effort is not what will run here (card #151): said beside the field,
  // which is highlighted. Never a silent swap.
  modelNotice?: string | null

  daemons: RunDaemon[]
  selectedDaemon: RunDaemon | null
  onDaemonChange: (d: RunDaemon) => void
  clusterConfigured: boolean

  // Engine availability, already resolved for the chosen runtime by the sheet
  // (cluster credentials + personal credentials, or the daemon's own list).
  availableEnginesHere: string[] | null
  engineChoices?: { id: string; name: string }[]
  engineDiscoveryState?: 'loading' | 'known' | 'unknown'
  engineDiscoveryError?: string
  onEngineDiscoveryRetry?: () => void

  // Preflight, computed by the sheet and rendered here under Where it runs.
  clusterEngineBlocked: boolean
  clusterGitMissing: boolean
  settingsLink: ReactNode
  // When the sheet knows exactly why the engine can't run here and what to
  // do about it, this replaces the generic "doesn't appear to have …" text.
  engineMissingNote?: ReactNode

  dangerouslySkipPermissions: boolean
  onSkipPermissionsChange: (v: boolean) => void

  // Repo-dependent hints ("will clone to …") that belong next to the daemon.
  daemonNotes?: ReactNode
  // The selected daemon's repos folder was changed from here. When given,
  // the folder is shown with a Change affordance under the daemon.
  onReposRootChanged?: (daemonId: string, reposRoot: string) => void
}

export default function RunColumn(props: RunColumnProps) {
  const {
    runtime, onRuntimeChange, kind, onKindChange, engine, onEngineChange,
    models, modelsLoading, modelsFallback, modelsError, modelsRefreshing, onModelsRetry, model, onModelChange, effort, onEffortChange, modelFreeText,
    daemons, selectedDaemon, onDaemonChange, clusterConfigured,
    availableEnginesHere, clusterEngineBlocked, clusterGitMissing, settingsLink, engineMissingNote,
    dangerouslySkipPermissions, onSkipPermissionsChange, daemonNotes,
    onReposRootChanged,
  } = props

  const engineMissingHere = availableEnginesHere !== null && !availableEnginesHere.includes(engine)
  const daemonChoice = daemons.length > 1 ||
    (daemons.length === 1 && !!selectedDaemon && selectedDaemon.id !== daemons[0].id)
  const sandboxImageMissing =
    runtime === 'docker' && !!selectedDaemon && !selectedDaemon.sandbox_available

  return (
    <div className="launch-col-run" data-testid="launch-col-run" data-phone-order="2">
      {/* 1. Where it runs */}
      <div style={{ marginBottom: 20 }}>
        <span style={sectionLabel}>Where it runs</span>
        <div role="radiogroup" aria-label="Where it runs">
          {RUNTIME_CARDS.map(card => {
            const reason = runtimeDisabledReason(card.id, { clusterConfigured, daemons, engine, kind })
            const selected = runtime === card.id
            return (
              <button
                key={card.id}
                type="button"
                role="radio"
                data-testid={`runtime-${card.id}`}
                aria-checked={selected}
                disabled={reason !== null}
                onClick={() => onRuntimeChange(card.id)}
                style={{
                  display: 'block',
                  width: '100%',
                  textAlign: 'left',
                  background: selected ? 'var(--selected-bg)' : 'var(--scree)',
                  border: `1px solid ${selected ? 'var(--selected-border)' : 'var(--stone)'}`,
                  borderRadius: 8,
                  padding: '10px 12px',
                  marginBottom: 6,
                  cursor: reason ? 'not-allowed' : 'pointer',
                  opacity: reason ? 0.55 : 1,
                  color: 'var(--chalk)',
                  fontFamily: 'inherit',
                }}
              >
                <span style={{ fontWeight: 600, fontSize: '0.9rem', color: selected ? 'var(--selected-text)' : 'var(--chalk)' }}>
                  {card.title}
                </span>
                <span style={{ display: 'block', fontSize: '0.75rem', marginTop: 3, color: 'var(--fog)' }}>
                  {card.body}
                </span>
                {reason && (
                  <span
                    data-testid={`runtime-${card.id}-reason`}
                    style={{ display: 'block', fontSize: '0.72rem', marginTop: 4, color: 'var(--fog-dim)' }}
                  >
                    {reason}
                  </span>
                )}
              </button>
            )
          })}
        </div>

        {/* The sandbox image is a "may fail", not a "cannot" — warn, don't block. */}
        {sandboxImageMissing && (
          <p data-testid="sandbox-image-warning" style={warnStyle}>
            ⚠ {selectedDaemon!.name} hasn't built the sandbox image yet — this launch may fail. See
            DAEMON.md to build it.
          </p>
        )}

        {/* Daemon picker — only worth showing when there is a choice to make,
            and only when the chosen runtime actually uses a daemon. A picked
            daemon that has since gone (the launch sheet opened from what it
            last knew) is a choice to make again, even with one left. */}
        {daemonChoice && runtime !== 'cluster' && (
          <div data-testid="daemon-picker" style={{ marginTop: 10 }}>
            <span style={sectionLabel}>Daemon</span>
            {daemons.map(d => (
              <button
                key={d.id}
                type="button"
                role="radio"
                data-testid={`daemon-${d.id}`}
                aria-checked={selectedDaemon?.id === d.id}
                onClick={() => onDaemonChange(d)}
                style={{
                  display: 'block',
                  width: '100%',
                  textAlign: 'left',
                  background: selectedDaemon?.id === d.id ? 'var(--selected-bg)' : 'var(--scree)',
                  border: `1px solid ${selectedDaemon?.id === d.id ? 'var(--selected-border)' : 'var(--stone)'}`,
                  borderRadius: 8,
                  padding: '8px 12px',
                  marginBottom: 6,
                  cursor: 'pointer',
                  color: 'var(--chalk)',
                  fontFamily: 'inherit',
                  fontSize: '0.9rem',
                  fontWeight: 600,
                }}
              >
                {d.name}
              </button>
            ))}
          </div>
        )}
        {daemons.length === 1 && !daemonChoice && runtime !== 'cluster' && (
          <p data-testid="daemon-single" style={noteStyle}>on {daemons[0].name}</p>
        )}
        {daemons.length === 0 && (
          <div style={{ color: 'var(--fog)', fontSize: '0.85rem', padding: '8px 0' }}>
            {clusterConfigured
              ? 'No workstation daemon connected — sessions will run as cluster pods.'
              : 'No daemon connected. Start it: ./daemon/install.sh status (from install/desktop), or see DAEMON.md.'}
          </div>
        )}

        {runtime !== 'cluster' && selectedDaemon && onReposRootChanged && (
          <ReposRootEditor
            daemonId={selectedDaemon.id}
            daemonName={selectedDaemon.name}
            reposRoot={selectedDaemon.repos_root}
            onChanged={root => onReposRootChanged(selectedDaemon.id, root)}
          />
        )}

        {daemonNotes}

        {/* Cluster preflight lives under Where it runs — the choice it is about. */}
        {clusterEngineBlocked && (
          <p data-testid="cluster-engine-block" style={{ ...warnStyle, fontSize: '0.78rem', padding: '8px 10px' }}>
            Add a {ENGINE_LABELS[engine] ?? engine} credential in {settingsLink} before launching a
            cluster session.
          </p>
        )}
        {clusterGitMissing && (
          <p
            data-testid="cluster-git-warning"
            style={{
              fontSize: '0.78rem',
              color: 'var(--fog)',
              background: 'var(--scree)',
              border: '1px solid var(--stone)',
              borderRadius: 6,
              padding: '8px 10px',
              margin: '8px 0 0',
            }}
          >
            No GitHub credential — this session can only clone public repositories and cannot push.
            Add one in {settingsLink}.
          </p>
        )}
      </div>

      {/* 2. Session type */}
      <div style={{ marginBottom: 20 }}>
        <span style={sectionLabel}>Session type</span>
        <div role="radiogroup" aria-label="Session type" style={{ display: 'flex', gap: 6 }}>
          <button
            type="button"
            role="radio"
            data-testid="kind-agent"
            aria-checked={kind === 'agent'}
            onClick={() => onKindChange('agent')}
            style={pillStyle(kind === 'agent')}
          >
            Agent
          </button>
        </div>
      </div>

      {/* Only target-available engines are actionable; a sole engine needs no selector. */}
      <div style={{ marginBottom: 20 }}>
        {props.engineDiscoveryState === 'loading' || (availableEnginesHere === null && props.engineDiscoveryState !== 'unknown') ? (
          <p role="status" style={noteStyle}>Checking available engines…</p>
        ) : props.engineDiscoveryState === 'unknown' ? (
          <p role="status" style={warnStyle}>{props.engineDiscoveryError ?? 'Engine availability is unknown.'}{' '}
            {props.onEngineDiscoveryRetry && <button type="button" onClick={props.onEngineDiscoveryRetry}>Retry engines</button>}
          </p>
        ) : (availableEnginesHere ?? []).length === 0 ? (
          <p role="status" style={warnStyle}>No engines available for this runtime. Connect an engine in Settings or choose another runtime.</p>
        ) : (availableEnginesHere ?? []).length > 1 ? (
          <>
            <span style={sectionLabel}>Engine</span>
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {(props.engineChoices ?? (availableEnginesHere ?? []).map(id => ({ id, name: ENGINE_LABELS[id] ?? id }))).map(choice => (
                <button key={choice.id} type="button" data-testid={`engine-${choice.id}`} aria-pressed={engine === choice.id}
                  onClick={() => onEngineChange(choice.id)} style={pillStyle(engine === choice.id)}>
                  {choice.name}
                </button>
              ))}
            </div>
          </>
        ) : null}
        {props.engineDiscoveryState === 'known' && props.engineDiscoveryError && <p role="status" style={noteStyle}>
          {props.engineDiscoveryError}{' '}
          {props.onEngineDiscoveryRetry && <button type="button" onClick={props.onEngineDiscoveryRetry}>Retry engines</button>}
        </p>}
        {/* OpenClaw pins the session to the daemon host — with no daemon
            connected that leaves all three cards greyed out and no
            explanation of the dead end, so say it once, here. */}
        {engine === 'openclaw' && daemons.length === 0 && (
          <p data-testid="openclaw-no-daemon" style={warnStyle}>
            ⚠ OpenClaw needs a connected daemon
          </p>
        )}
        {/* Suppressed when clusterEngineBlocked: that case already got the
            precise, actionable message above. */}
        {engineMissingHere && !clusterEngineBlocked && (
          <p data-testid="engine-missing-warning" style={warnStyle}>
            ⚠ {engineMissingNote ?? (runtime === 'cluster'
              ? `Cluster runtime has no credentials configured for ${engine} — this launch will likely fail. See CLUSTER-RUNTIME.md, or pick a different engine.`
              : `${selectedDaemon?.name ?? 'This daemon'} doesn't appear to have ${engine} configured — this launch may fail. Pick a different engine, or configure it there first.`)}
          </p>
        )}
      </div>

      {/* 4. Model + effort — whatever the engine's model source returned
          (GET /api/models/{engine}); an engine with no list shows nothing —
          or, when the sheet passes modelFreeText, an optional model name box
          — and uses its own configured default. */}
      {(props.engineDiscoveryState === undefined || props.engineDiscoveryState === 'known' && !engineMissingHere) && <>
      {modelsLoading && models.length === 0 && (
        <p role="status" style={noteStyle}>Loading {props.engineChoices?.find(e => e.id === engine)?.name ?? ENGINE_LABELS[engine] ?? engine} models…</p>
      )}
      {modelsRefreshing && <p role="status" style={noteStyle}>Refreshing {props.engineChoices?.find(e => e.id === engine)?.name ?? ENGINE_LABELS[engine] ?? engine} models… You can launch with the default model.</p>}
      {modelsError && <p role="status" data-testid="models-error" style={noteStyle}>
        {modelsError}{' '}
        {onModelsRetry && <button type="button" onClick={onModelsRetry}>Retry</button>}
      </p>}
      {props.modelNotice && (
        <p role="status" data-testid="model-keep-notice" className="model-keep-notice"
          style={{ ...noteStyle, margin: '0 0 6px', padding: '4px 8px', borderLeft: '3px solid var(--amber)', background: 'var(--scree-2)', color: 'var(--chalk)' }}>
          {props.modelNotice}
        </p>
      )}
      <div data-testid="model-field" data-notice={props.modelNotice ? 'true' : undefined}
        style={props.modelNotice ? { outline: '2px solid var(--amber)', outlineOffset: 2, borderRadius: 6 } : undefined}>
      <ModelPicker
        models={models}
        loading={modelsLoading}
        fallback={modelsFallback}
        model={model}
        onModelChange={onModelChange}
        effort={effort}
        onEffortChange={onEffortChange}
        freeText={modelFreeText}
        labelStyle={sectionLabel}
        noteStyle={noteStyle}
        pillStyle={s => pillStyle(s)}
      />
      </div>

      </>}

      {/* 5. Permissions */}
      <div style={{ marginBottom: 20 }}>
        <span style={sectionLabel}>Permissions</span>
        {kind === 'agent' ? (
          <p data-testid="agent-permissions-note" style={{ ...noteStyle, margin: 0 }}>{AGENT_NO_PROMPTS}</p>
        ) : runtime === 'docker' && engine !== 'openclaw' ? (
          <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
            <div
              data-testid="skip-permissions-toggle"
              role="switch"
              aria-checked={dangerouslySkipPermissions}
              // A div playing switch is not focusable or operable by default —
              // this one is the most consequential control in the column, so
              // it gets the keyboard contract a real checkbox would have.
              tabIndex={0}
              onClick={() => onSkipPermissionsChange(!dangerouslySkipPermissions)}
              onKeyDown={e => {
                if (e.key === ' ' || e.key === 'Enter') {
                  e.preventDefault()
                  onSkipPermissionsChange(!dangerouslySkipPermissions)
                }
              }}
              title={engine === 'codex' ? '--dangerously-bypass-approvals-and-sandbox' : engine === 'hermes' ? '--yolo' : '--dangerously-skip-permissions'}
              style={{
                width: 36,
                height: 20,
                borderRadius: 10,
                background: dangerouslySkipPermissions ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
                border: `1px solid ${dangerouslySkipPermissions ? 'var(--blaze)' : 'var(--stone)'}`,
                position: 'relative',
                flexShrink: 0,
                cursor: 'pointer',
                transition: 'background 0.15s, border-color 0.15s',
              }}
            >
              <div style={{
                width: 14,
                height: 14,
                borderRadius: '50%',
                background: dangerouslySkipPermissions ? 'var(--amber)' : 'var(--fog-dim)',
                position: 'absolute',
                top: 2,
                left: dangerouslySkipPermissions ? 18 : 2,
                transition: 'left 0.15s, background 0.15s',
              }} />
            </div>
            <span style={{ fontSize: '0.85rem', color: dangerouslySkipPermissions ? 'var(--chalk)' : 'var(--fog)' }}>
              Bypass all permission prompts (Docker sandbox only)
            </span>
          </label>
        ) : (
          <p style={{ ...noteStyle, margin: 0 }}>Permission prompts stay on.</p>
        )}
      </div>
    </div>
  )
}
