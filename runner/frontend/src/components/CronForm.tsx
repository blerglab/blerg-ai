// CronForm: create or edit a cron (spec 7.7, 8). The schedule is a preset (every day, weekdays or
// one weekday, at one or several times) or an advanced five-field expression; the server checks
// it and its message is shown as it comes. The MCP section is the tool picker with every tool's
// mode chosen by hand: nothing defaults on and there is no "select all".
import { useEffect, useMemo, useState } from 'react'
import type { CSSProperties } from 'react'
import { apiFetch } from '../apiFetch'
import { listBoards } from '../lib/boardApi'
import { buildExpression, describeSchedule, hasFiveFields, parseExpression, DAY_NAMES } from '../lib/cronSchedule'
import type { PresetKind } from '../lib/cronSchedule'
import { useEngineModels } from '../lib/engineModels'
import { FOCUS_BOARD_PROMPT } from '../lib/focusPrompt'
import type { Board, CronInfo, CronRuntime, McpSelectionEntry } from '../types'
import type { RunDaemon } from '../lib/runtimes'
import McpPicker from './McpPicker'
import ModelPicker from './ModelPicker'

interface Props {
  cron?: CronInfo
  onSaved: (cron: CronInfo) => void
  onCancel: () => void
}

type Kind = PresetKind | 'advanced'

const label: CSSProperties = { color: 'var(--fog)', fontSize: '0.7rem', fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', marginBottom: 6, display: 'block' }
const note: CSSProperties = { fontSize: '0.72rem', color: 'var(--fog-dim)', margin: '6px 0 0' }
const field: CSSProperties = { marginBottom: 20 }
const control: CSSProperties = {
  width: '100%', background: 'var(--scree)', border: '1px solid var(--stone)', borderRadius: 8, color: 'var(--chalk)',
  padding: '8px 12px', boxSizing: 'border-box', fontFamily: 'inherit',
}
const pill = (selected: boolean): CSSProperties => ({
  background: selected ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
  border: `1px solid ${selected ? 'var(--blaze)' : 'var(--stone)'}`,
  borderRadius: 6, color: selected ? 'var(--amber)' : 'var(--fog)', padding: '5px 14px', cursor: 'pointer',
  fontSize: '0.85rem', fontFamily: 'inherit', fontWeight: selected ? 700 : 400,
})

const DEFAULT_TIME = '08:00'
const MAX_TIMES = 6
const DEFAULT_GRACE_MIN = 60
const DEFAULT_MAX_RUNTIME_MIN = 30

const browserZone = () => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' }
}

function zoneList(current: string): string[] {
  let zones: string[] = []
  try {
    const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf
    if (f) zones = f('timeZone')
  } catch { /* fall back to the short list */ }
  if (zones.length === 0) zones = ['America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'Europe/London', 'Europe/Paris', 'Asia/Tokyo', 'Australia/Sydney']
  const set = new Set(zones)
  set.add('UTC')
  set.add(current)
  return [...set].sort()
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = await res.json() as { error?: string }
    if (body.error) return body.error
  } catch { /* fall through */ }
  return `HTTP ${res.status}`
}

// Whether two MCP selections mean the same thing, whatever the order of connections and tools.
function sameSelection(a: McpSelectionEntry[], b: McpSelectionEntry[]): boolean {
  const canon = (v: McpSelectionEntry[]) => JSON.stringify(
    [...v]
      .sort((x, y) => x.connection.localeCompare(y.connection))
      .map(e => [e.connection, Object.entries(e.tools).sort(([p], [q]) => p.localeCompare(q)).map(([n, t]) => [n, t.mode, t.hash])]),
  )
  return canon(a) === canon(b)
}

// The state the schedule section starts from: a cron's own, or every day at 08:00.
function initialSchedule(cron?: CronInfo): { kind: Kind; times: string[]; dow: number; expr: string } {
  if (!cron) return { kind: 'daily', times: [DEFAULT_TIME], dow: 1, expr: '' }
  const p = parseExpression(cron.schedule)
  if (!p) return { kind: 'advanced', times: [DEFAULT_TIME], dow: 1, expr: cron.schedule }
  return { kind: p.kind, times: p.times, dow: p.dow, expr: cron.schedule }
}

export default function CronForm({ cron, onSaved, onCancel }: Props) {
  const initial = useMemo(() => initialSchedule(cron), [cron])
  const [name, setName] = useState(cron?.name ?? '')
  const [prompt, setPrompt] = useState(cron?.prompt ?? '')
  const [kind, setKind] = useState<Kind>(initial.kind)
  const [times, setTimes] = useState<string[]>(initial.times)
  const [dow, setDow] = useState(initial.dow)
  const [expr, setExpr] = useState(initial.expr)
  const [timezone, setTimezone] = useState(cron?.timezone ?? browserZone())
  const [model, setModel] = useState(cron?.model ?? '')
  const [effort, setEffort] = useState(cron?.effort ?? '')
  const [runtime, setRuntime] = useState<CronRuntime>(cron?.runtime ?? 'auto')
  const [daemonId, setDaemonId] = useState(cron?.daemon_id ?? '')
  const [boardId, setBoardId] = useState(cron?.board_id ?? '')
  // The catch-up window is shown in minutes when it is a whole number of them, otherwise in
  // seconds: a stored 90 s is never rounded to 120 s by opening and saving the form.
  const initialGrace = cron?.grace_seconds ?? DEFAULT_GRACE_MIN * 60
  const [graceUnit, setGraceUnit] = useState<'minutes' | 'seconds'>(initialGrace % 60 === 0 ? 'minutes' : 'seconds')
  const [graceValue, setGraceValue] = useState(String(initialGrace % 60 === 0 ? initialGrace / 60 : initialGrace))
  const [maxRuntimeMin, setMaxRuntimeMin] = useState(String(Math.round((cron?.max_runtime_seconds ?? DEFAULT_MAX_RUNTIME_MIN * 60) / 60)))
  const [mcp, setMcp] = useState<McpSelectionEntry[]>(cron?.mcp ?? [])
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [confirmStarter, setConfirmStarter] = useState(false)

  const [daemons, setDaemons] = useState<RunDaemon[]>([])
  const [boards, setBoards] = useState<Board[] | null>(null)
  useEffect(() => {
    let live = true
    apiFetch('/api/daemons')
      .then(r => (r.ok ? r.json() : []))
      .then(d => { if (live && Array.isArray(d)) setDaemons(d as RunDaemon[]) })
      .catch(() => { /* the pin is optional */ })
    listBoards()
      .then(b => { if (live) setBoards(b) })
      .catch(() => { if (live) setBoards(null) })
    return () => { live = false }
  }, [])

  const engineModels = useEngineModels('claude')
  const zones = useMemo(() => zoneList(timezone), [timezone])

  const built = kind === 'advanced' ? null : buildExpression({ kind, times, dow })
  const expression = kind === 'advanced' ? expr.trim() : built && 'expr' in built ? built.expr : ''
  const scheduleProblem = kind === 'advanced'
    ? (hasFiveFields(expr) ? null : 'The schedule needs exactly five fields (minute hour day-of-month month day-of-week).')
    : built && 'error' in built ? built.error : null

  function setTimeCount(n: number) {
    setTimes(prev => Array.from({ length: n }, (_, i) => prev[i] ?? prev[prev.length - 1] ?? DEFAULT_TIME))
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    if (!name.trim()) return setError('Give the cron a name.')
    if (!prompt.trim()) return setError('Write the prompt the agent should run.')
    if (scheduleProblem) return setError(scheduleProblem)
    const maxRun = Number(maxRuntimeMin)
    // An empty catch-up field means "not set": a create gets the server default, an edit keeps the
    // stored value. Only a typed number (0 included) is sent.
    let graceSeconds: number | null = null
    if (graceValue.trim() !== '') {
      const g = Number(graceValue) * (graceUnit === 'minutes' ? 60 : 1)
      if (!Number.isFinite(g) || Math.abs(g - Math.round(g)) > 1e-6 || g < 0 || g > 24 * 3600) {
        return setError('The catch-up window must be a whole number of seconds between 0 and 1440 minutes.')
      }
      graceSeconds = Math.round(g)
    }
    if (!Number.isFinite(maxRun) || maxRun < 1 || maxRun > 24 * 60) return setError('The maximum run time must be between 1 and 1440 minutes.')

    const body: Record<string, unknown> = {
      name: name.trim(), schedule: expression, timezone, prompt, runtime, max_runtime_seconds: Math.round(maxRun * 60),
    }
    if (graceSeconds !== null) body.grace_seconds = graceSeconds
    // A create always names its tools (possibly none). An edit sends `mcp` only when the person
    // changed it: the server checks a sent value against the live connections, and a cron whose
    // connection is gone or down must stay editable for a prompt or schedule change.
    if (!cron || !sameSelection(mcp, cron.mcp)) body.mcp = mcp
    if (cron) {
      // A PATCH: an empty string clears the field.
      body.model = model
      body.effort = model ? effort : ''
      body.daemon_id = runtime === 'cluster' ? '' : daemonId
      body.board_id = boardId
    } else {
      if (model) body.model = model
      if (model && effort) body.effort = effort
      if (daemonId && runtime !== 'cluster') body.daemon_id = daemonId
      if (boardId) body.board_id = boardId
    }
    setSaving(true)
    try {
      const res = await apiFetch(cron ? `/api/crons/${encodeURIComponent(cron.id)}` : '/api/crons', {
        method: cron ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        setError(await errorText(res))
        return
      }
      onSaved(await res.json() as CronInfo)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  function insertStarterPrompt() {
    if (prompt.trim()) {
      setConfirmStarter(true)
      return
    }
    setPrompt(FOCUS_BOARD_PROMPT)
  }

  const showTimes = kind !== 'advanced'
  const timeCount = kind === 'weekly' ? 1 : times.length
  const boardKnown = boards?.some(b => b.id === boardId) ?? false

  return (
    <form onSubmit={submit} data-testid="cron-form" style={{ padding: 16, maxWidth: 720 }}>
      <h2 style={{ color: 'var(--chalk)', fontSize: '1.05rem', margin: '0 0 16px' }}>{cron ? 'Edit cron' : 'New cron'}</h2>

      <div style={field}>
        <label style={label} htmlFor="cron-name">Name</label>
        <input id="cron-name" value={name} onChange={e => setName(e.target.value)} maxLength={100} placeholder="Morning digest" style={control} />
      </div>

      <div style={field}>
        <span style={label} id="cron-schedule-label">Schedule</span>
        <div role="radiogroup" aria-labelledby="cron-schedule-label" style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 10 }}>
          {([['daily', 'Every day'], ['weekdays', 'Weekdays'], ['weekly', 'Once a week'], ['advanced', 'Advanced']] as [Kind, string][]).map(([k, text]) => (
            <label key={k} htmlFor={`cron-kind-${k}`} style={pill(kind === k)}>
              <input
                id={`cron-kind-${k}`} type="radio" name="cron-kind" checked={kind === k}
                onChange={() => { setKind(k); if (k === 'weekly') setTimes(t => [t[0] ?? DEFAULT_TIME]) }}
                style={{ position: 'absolute', opacity: 0, pointerEvents: 'none' }}
              />
              {text}
            </label>
          ))}
        </div>
        {kind === 'weekly' && (
          <div style={{ marginBottom: 10 }}>
            <label style={label} htmlFor="cron-dow">Day of the week</label>
            <select id="cron-dow" value={dow} onChange={e => setDow(Number(e.target.value))} style={control}>
              {DAY_NAMES.map((d, i) => <option key={d} value={i}>{d}</option>)}
            </select>
          </div>
        )}
        {showTimes && (
          <div>
            {kind !== 'weekly' && (
              <div style={{ marginBottom: 10 }}>
                <label style={label} htmlFor="cron-times">Times a day</label>
                <select id="cron-times" value={timeCount} onChange={e => setTimeCount(Number(e.target.value))} style={control}>
                  {Array.from({ length: MAX_TIMES }, (_, i) => i + 1).map(n => <option key={n} value={n}>{n}</option>)}
                </select>
              </div>
            )}
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              {times.slice(0, timeCount).map((t, i) => (
                <div key={i}>
                  <label style={label} htmlFor={`cron-time-${i}`}>{timeCount > 1 ? `Time ${i + 1}` : 'Time'}</label>
                  <input
                    id={`cron-time-${i}`} type="time" value={t}
                    onChange={e => setTimes(prev => prev.map((x, j) => (j === i ? e.target.value : x)))}
                    style={{ ...control, width: 'auto' }}
                  />
                </div>
              ))}
            </div>
          </div>
        )}
        {kind === 'advanced' && (
          <div>
            <label style={label} htmlFor="cron-expression">Cron expression</label>
            <input
              id="cron-expression" value={expr} onChange={e => setExpr(e.target.value)} placeholder="*/30 * * * *"
              autoCapitalize="none" autoCorrect="off" spellCheck={false} style={{ ...control, fontFamily: 'monospace' }}
            />
            <p style={note}>Five fields: minute hour day-of-month month day-of-week. No @-shortcuts, no TZ= prefix, and never more often than every 15 minutes.</p>
          </div>
        )}
        <div style={{ marginTop: 10 }}>
          <label style={label} htmlFor="cron-timezone">Timezone</label>
          <select id="cron-timezone" value={timezone} onChange={e => setTimezone(e.target.value)} style={control}>
            {zones.map(z => <option key={z} value={z}>{z}</option>)}
          </select>
        </div>
        <p data-testid="cron-schedule-words" style={{ ...note, color: 'var(--fog)' }}>
          {expression && !scheduleProblem ? `${describeSchedule(expression)} (${timezone})` : scheduleProblem ?? ''}
        </p>
      </div>

      <div style={field}>
        <label style={label} htmlFor="cron-prompt">Prompt</label>
        <textarea
          id="cron-prompt" value={prompt} onChange={e => setPrompt(e.target.value)} rows={6} maxLength={16384}
          placeholder="What should the agent do each time this runs?" style={{ ...control, resize: 'vertical' }}
        />
        <div style={{ marginTop: 8, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <button type="button" onClick={insertStarterPrompt} style={pill(false)}>Use the focus-board starter prompt</button>
          {confirmStarter && (
            <span data-testid="focus-prompt-confirm" role="group" aria-label="Replace the prompt" style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: '0.8rem', color: 'var(--fog)' }}>
              Replace what you have written?
              <button type="button" onClick={() => { setPrompt(FOCUS_BOARD_PROMPT); setConfirmStarter(false) }} style={pill(true)}>Replace my prompt</button>
              <button type="button" onClick={() => setConfirmStarter(false)} style={pill(false)}>Keep my prompt</button>
            </span>
          )}
        </div>
        <p style={note}>Files your mail and calendar items as cards, keeps one card per item, and queues anything outward for your approval. It works with a board made from the focus-board template.</p>
      </div>

      <ModelPicker
        models={engineModels.models}
        loading={engineModels.loading}
        fallback={engineModels.fallback}
        model={model}
        onModelChange={id => { setModel(id); setEffort('') }}
        effort={effort}
        onEffortChange={setEffort}
        labelStyle={label}
        noteStyle={note}
        pillStyle={pill}
      />
      {engineModels.models.length > 0 && (
        <div style={{ ...field, marginTop: -8 }}>
          <button type="button" id="cron-model-default" onClick={() => { setModel(''); setEffort('') }} style={pill(model === '')}>
            Use the engine's default model
          </button>
        </div>
      )}

      <div style={field}>
        <label style={label} htmlFor="cron-runtime">Where it runs</label>
        <select id="cron-runtime" value={runtime} onChange={e => setRuntime(e.target.value as CronRuntime)} style={control}>
          <option value="auto">Automatic (cluster pod when there is a cluster, else the local sandbox)</option>
          <option value="cluster">Cluster pod</option>
          <option value="docker">Local sandbox (Docker on a connected daemon)</option>
        </select>
        <p style={note}>A cron never runs directly on a machine: only in a cluster pod or the Docker sandbox.</p>
        {runtime !== 'cluster' && (
          <div style={{ marginTop: 10 }}>
            <label style={label} htmlFor="cron-daemon">Pin to a daemon (optional)</label>
            <select id="cron-daemon" value={daemonId} onChange={e => setDaemonId(e.target.value)} style={control}>
              <option value="">Any connected daemon</option>
              {daemons.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}
              {daemonId && !daemons.some(d => d.id === daemonId) && <option value={daemonId}>{daemonId}</option>}
            </select>
          </div>
        )}
      </div>

      <div style={field}>
        <label style={label} htmlFor="cron-board">Target board (optional)</label>
        {boards ? (
          <select id="cron-board" value={boardId} onChange={e => setBoardId(e.target.value)} style={control}>
            <option value="">None</option>
            {boards.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
            {boardId && !boardKnown && <option value={boardId}>{boardId}</option>}
          </select>
        ) : (
          <input id="cron-board" value={boardId} onChange={e => setBoardId(e.target.value)} placeholder="board id" style={control} />
        )}
        <p data-testid="cron-board-template-note" style={note}>
          For the starter prompt, create the board in the board app with the Focus board template (New board, then Template), then choose it here.
        </p>
      </div>

      <div style={field}>
        <span style={label}>MCP tools</span>
        <div data-testid="cron-mcp">
          <McpPicker value={mcp} onChange={setMcp} requireExplicit />
        </div>
        <p style={note}>Nothing is on by default. Turn on each tool you want this cron to use; a tool left off is not available to it.</p>
      </div>

      <div style={{ ...field, display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <div>
          <label style={label} htmlFor="cron-grace">Catch-up window</label>
          <div style={{ display: 'flex', gap: 6 }}>
            <input
              id="cron-grace" type="number" min={0} step="any" max={graceUnit === 'minutes' ? 1440 : 86400} value={graceValue}
              placeholder="default" onChange={e => setGraceValue(e.target.value)} style={{ ...control, width: 100 }}
            />
            <select
              id="cron-grace-unit" aria-label="Catch-up window unit" value={graceUnit} style={control}
              onChange={e => {
                const next = e.target.value as 'minutes' | 'seconds'
                const n = Number(graceValue)
                if (graceValue.trim() !== '' && Number.isFinite(n)) setGraceValue(String(next === 'seconds' ? n * 60 : n / 60))
                setGraceUnit(next)
              }}
            >
              <option value="minutes">minutes</option>
              <option value="seconds">seconds</option>
            </select>
          </div>
        </div>
        <div>
          <label style={label} htmlFor="cron-max-runtime">Maximum run time (minutes)</label>
          <input id="cron-max-runtime" type="number" min={1} max={1440} value={maxRuntimeMin} onChange={e => setMaxRuntimeMin(e.target.value)} style={{ ...control, width: 140 }} />
        </div>
      </div>

      <div data-testid="cron-notes" style={{ ...field, border: '1px solid var(--stone)', borderRadius: 8, padding: '10px 14px', background: 'var(--scree)' }}>
        <span style={label}>Good to know</span>
        <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--fog)', fontSize: '0.8rem', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <li>{'A cron run has no shell or web tools: it can work with files in its own scratch folder and use the MCP tools you turn on above.'}</li>
          <li>{'Sessions started by a cron are private to you: no other account on this install can see them.'}</li>
          <li>{"On a desktop, a run in the local sandbox uses the host developer's own Claude login, not a personal credential, and can use any connected daemon; every account on an install shares the connected daemons."}</li>
          <li>{'The cron holds its own access token, which expires after a year and can be renewed from the list. Deleting or pausing the cron revokes it.'}</li>
        </ul>
      </div>

      {error && (
        <p role="alert" data-testid="cron-form-error" style={{ color: 'var(--danger)', fontSize: '0.85rem', margin: '0 0 12px' }}>{error}</p>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="submit" disabled={saving} style={{ ...pill(true), padding: '8px 18px' }}>{cron ? 'Save changes' : 'Create cron'}</button>
        <button type="button" onClick={onCancel} style={{ ...pill(false), padding: '8px 18px' }}>Cancel</button>
      </div>
    </form>
  )
}
