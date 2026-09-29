import type { CSSProperties } from 'react'
import type { EngineModel } from '../types'
import { hasAutoEffort } from '../lib/engineModels'

// ModelPicker renders whatever model list it is given, for any engine: the
// "main" models as pills, the "overflow" ones behind a "More models" select,
// and — only when the selected model has effort levels — an Effort row, led
// by an "Auto" choice (sends no effort) when the model has no default of its
// own. It knows nothing about any particular engine; the list comes from
// useEngineModels (GET /api/models/{engine}). An empty list renders nothing,
// or — when the sheet passes freeText (an engine whose CLI takes a model name
// but reported no list) — an optional free-text model name box, with an
// Effort row (Auto first) when the engine takes effort.

export interface ModelPickerFreeText {
  value: string
  onChange: (v: string) => void
  // true when value is non-empty and not a model name the engine accepts.
  invalid: boolean
  // The engine's effort allowlist ([] = no effort control), the chosen one
  // (undefined = Auto) and its setter ("" for Auto).
  efforts?: readonly string[]
  effort?: string
  onEffortChange?: (effort: string) => void
}

export interface ModelPickerProps {
  models: readonly EngineModel[]
  loading?: boolean
  fallback?: boolean
  model: string
  onModelChange: (id: string) => void
  effort?: string
  // Called with "" for Auto.
  onEffortChange?: (effort: string) => void
  freeText?: ModelPickerFreeText
  labelStyle: CSSProperties
  noteStyle: CSSProperties
  pillStyle: (selected: boolean) => CSSProperties
}

export default function ModelPicker({
  models, loading, fallback, model, onModelChange, effort, onEffortChange, freeText, labelStyle, noteStyle, pillStyle,
}: ModelPickerProps) {
  if (models.length === 0) {
    if (!freeText || loading) return null
    return (
      <div style={{ marginBottom: 20 }} data-testid="model-free-text">
        <label style={labelStyle} htmlFor="launch-model-free-text">Model name (optional)</label>
        <input
          id="launch-model-free-text"
          data-testid="model-free-text-input"
          value={freeText.value}
          onChange={e => freeText.onChange(e.target.value)}
          placeholder="the engine's own default"
          autoCorrect="off"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={freeText.invalid}
          style={{
            width: '100%',
            background: 'var(--scree)',
            border: `1px solid ${freeText.invalid ? 'var(--danger)' : 'var(--stone)'}`,
            borderRadius: 8,
            color: 'var(--chalk)',
            padding: '8px 12px',
            boxSizing: 'border-box',
            fontFamily: 'inherit',
          }}
        />
        {freeText.invalid ? (
          <p data-testid="model-free-text-invalid" style={{ ...noteStyle, color: 'var(--danger)' }}>
            Not a model name: letters, digits and . _ : / @ - [ ], not starting with '-'.
          </p>
        ) : (
          <p style={noteStyle}>No model list was reported for this engine here — leave empty for its configured default.</p>
        )}
        {freeText.efforts && freeText.efforts.length > 0 && (
          <EffortRow
            label="Effort"
            efforts={freeText.efforts}
            auto
            effort={freeText.effort}
            onEffortChange={freeText.onEffortChange}
            labelStyle={labelStyle}
            pillStyle={pillStyle}
          />
        )}
      </div>
    )
  }
  const main = models.filter(m => m.section === 'main')
  const overflow = models.filter(m => m.section === 'overflow')
  const selected = models.find(m => m.id === model)
  const overflowSelected = selected?.section === 'overflow'
  const auto = hasAutoEffort(selected)
  return (
    <div style={{ marginBottom: 20 }} data-testid="model-picker">
      <span style={labelStyle}>Model</span>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {main.map(m => (
          <button
            key={m.id}
            type="button"
            data-testid={`model-${m.id}`}
            aria-pressed={model === m.id}
            title={m.description || m.id}
            onClick={() => onModelChange(m.id)}
            style={pillStyle(model === m.id)}
          >
            {m.name}
          </button>
        ))}
        {overflow.length > 0 && (
          <select
            data-testid="model-more"
            aria-label="More models"
            value={overflowSelected ? model : ''}
            onChange={e => { if (e.target.value) onModelChange(e.target.value) }}
            style={{ ...pillStyle(overflowSelected), paddingRight: 8 }}
          >
            <option value="">More models…</option>
            {overflow.map(m => (
              <option key={m.id} value={m.id} data-testid={`model-${m.id}`} title={m.description || m.id}>
                {m.name}
              </option>
            ))}
          </select>
        )}
      </div>
      {loading && <p data-testid="models-loading" style={noteStyle}>Loading models…</p>}
      {fallback && (
        <p data-testid="models-fallback" style={noteStyle}>
          Couldn't load the current model list — showing the built-in one.
        </p>
      )}
      {selected && selected.efforts.length > 0 && (
        <EffortRow
          label={selected.effort_kind ? `Effort (${selected.effort_kind})` : 'Effort'}
          efforts={selected.efforts}
          auto={auto}
          effort={effort}
          onEffortChange={onEffortChange}
          titleFor={e => (e === selected.default_effort ? `${e} (default for ${selected.name})` : e)}
          labelStyle={labelStyle}
          pillStyle={pillStyle}
        />
      )}
    </div>
  )
}

// EffortRow: the effort pills, led by "Auto" (no effort sent) when auto.
function EffortRow({ label, efforts, auto, effort, onEffortChange, titleFor, labelStyle, pillStyle }: {
  label: string
  efforts: readonly string[]
  auto: boolean
  effort?: string
  onEffortChange?: (effort: string) => void
  titleFor?: (e: string) => string
  labelStyle: CSSProperties
  pillStyle: (selected: boolean) => CSSProperties
}) {
  return (
    <div style={{ marginTop: 12 }} data-testid="effort-row">
      <span style={labelStyle}>{label}</span>
      <div role="radiogroup" aria-label="Effort" style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {auto && (
          <button
            type="button"
            role="radio"
            data-testid="effort-auto"
            aria-checked={!effort}
            title="Don't pass an effort — the engine's own configuration decides"
            onClick={() => onEffortChange?.('')}
            style={pillStyle(!effort)}
          >
            Auto
          </button>
        )}
        {efforts.map(e => (
          <button
            key={e}
            type="button"
            role="radio"
            data-testid={`effort-${e}`}
            aria-checked={effort === e}
            title={titleFor ? titleFor(e) : e}
            onClick={() => onEffortChange?.(e)}
            style={pillStyle(effort === e)}
          >
            {e}
          </button>
        ))}
      </div>
    </div>
  )
}
