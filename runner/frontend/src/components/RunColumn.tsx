import type { ReactNode, CSSProperties } from 'react'
import {
  RUNTIME_CARDS,
  ENGINE_LABELS,
  TERMINAL_NEEDS_DAEMON,
  AGENT_NO_PROMPTS,
  ACK_AGENT,
  ACK_TERMINAL,
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
    background: selected ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
    border: `1px ${dimmed ? 'dashed' : 'solid'} ${selected ? 'var(--blaze)' : dimmed ? 'var(--fog-dim)' : 'var(--stone)'}`,
    borderRadius: 6,
    color: selected ? 'var(--amber)' : dimmed ? 'var(--fog-dim)' : 'var(--fog)',
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
  model: string
  onModelChange: (id: string) => void
  effort?: string
  onEffortChange?: (e: string) => void
  // Free-text model name, offered by the picker when no list was reported.
  modelFreeText?: ModelPickerFreeText

  daemons: RunDaemon[]
  selectedDaemon: RunDaemon | null
  onDaemonChange: (d: RunDaemon) => void
  clusterConfigured: boolean

  // Engine availability, already resolved for the chosen runtime by the sheet
  // (cluster credentials + personal credentials, or the daemon's own list).
  availableEnginesHere: string[] | null

  // Preflight, computed by the sheet and rendered here under Where it runs.
  clusterEngineBlocked: boolean
  clusterGitMissing: boolean
  settingsLink: ReactNode
  // When the sheet knows exactly why the engine can't run here and what to
  // do about it, this replaces the generic "doesn't appear to have …" text.
  engineMissingNote?: ReactNode

  dangerouslySkipPermissions: boolean
  onSkipPermissionsChange: (v: boolean) => void
  ack: boolean
  onAckChange: (v: boolean) => void

  // Repo-dependent hints ("will clone to …") that belong next to the daemon.
  daemonNotes?: ReactNode
  // The selected daemon's repos folder was changed from here. When given,
  // the folder is shown with a Change affordance under the daemon.
  onReposRootChanged?: (daemonId: string, reposRoot: string) => void
}

export default function RunColumn(props: RunColumnProps) {
  const {
    runtime, onRuntimeChange, kind, onKindChange, engine, onEngineChange,
    models, modelsLoading, modelsFallback, model, onModelChange, effort, onEffortChange, modelFreeText,
    daemons, selectedDaemon, onDaemonChange, clusterConfigured,
    availableEnginesHere, clusterEngineBlocked, clusterGitMissing, settingsLink, engineMissingNote,
    dangerouslySkipPermissions, onSkipPermissionsChange, ack, onAckChange, daemonNotes,
    onReposRootChanged,
  } = props

  const engineMissingHere = availableEnginesHere !== null && !availableEnginesHere.includes(engine)
  const sandboxImageMissing =
    runtime === 'docker' && !!selectedDaemon && !selectedDaemon.sandbox_available
  const terminalDisabledReason = runtime === 'cluster' ? TERMINAL_NEEDS_DAEMON : null

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
                  background: selected ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
                  border: `1px solid ${selected ? 'var(--blaze)' : 'var(--stone)'}`,
                  borderRadius: 8,
                  padding: '10px 12px',
                  marginBottom: 6,
                  cursor: reason ? 'not-allowed' : 'pointer',
                  opacity: reason ? 0.55 : 1,
                  color: 'var(--chalk)',
                  fontFamily: 'inherit',
                }}
              >
                <span style={{ fontWeight: 600, fontSize: '0.9rem', color: selected ? 'var(--amber)' : 'var(--chalk)' }}>
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
            and only when the chosen runtime actually uses a daemon. */}
        {daemons.length > 1 && runtime !== 'cluster' && (
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
                  background: selectedDaemon?.id === d.id ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
                  border: `1px solid ${selectedDaemon?.id === d.id ? 'var(--blaze)' : 'var(--stone)'}`,
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
        {daemons.length === 1 && runtime !== 'cluster' && (
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
          {([['agent', 'Agent'], ['tmux', 'Terminal']] as const).map(([k, label]) => {
            const disabled = k === 'tmux' && terminalDisabledReason !== null
            return (
              <button
                key={k}
                type="button"
                role="radio"
                data-testid={`kind-${k}`}
                aria-checked={kind === k}
                disabled={disabled}
                onClick={() => onKindChange(k)}
                style={{ ...pillStyle(kind === k), cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.5 : 1 }}
              >
                {label}
              </button>
            )
          })}
        </div>
        {terminalDisabledReason && (
          <p data-testid="terminal-disabled-reason" style={noteStyle}>{terminalDisabledReason}</p>
        )}
      </div>

      {/* 3. Engine — buttons for an engine this runtime can't run stay
          clickable (the server is the real gate) but get a dimmed, dashed
          treatment plus the warning below. */}
      <div style={{ marginBottom: 20 }}>
        <span style={sectionLabel}>Engine</span>
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {(['claude', 'codex', 'hermes', 'openclaw'] as const).map(k => {
            const unavailable = availableEnginesHere !== null && !availableEnginesHere.includes(k)
            return (
              <button
                key={k}
                type="button"
                data-testid={`engine-${k}`}
                aria-pressed={engine === k}
                onClick={() => onEngineChange(k)}
                style={pillStyle(engine === k, unavailable)}
              >
                {ENGINE_LABELS[k]}
              </button>
            )
          })}
        </div>
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

      {/* 6. Acknowledgement — only where the session genuinely runs as you,
          on your machine, with nothing between it and your filesystem. */}
      {runtime === 'daemon' && (
        <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', fontSize: '0.8rem', color: 'var(--amber)', marginBottom: 16 }}>
          <input
            type="checkbox"
            data-testid="agent-ack"
            checked={ack}
            onChange={e => onAckChange(e.target.checked)}
          />
          {kind === 'agent' ? ACK_AGENT : ACK_TERMINAL}
        </label>
      )}
    </div>
  )
}
