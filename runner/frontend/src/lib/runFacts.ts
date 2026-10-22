// What a Running now row's hovercard says (card #129): the kind, engine and runtime, the model in
// full (its id in the tooltip), the state and the whole current step, elapsed time and tokens, the
// card it is on and who started it. Pure: SessionRail and ChildSessions pass what they know.
import { modelDisplayName, type RunTarget } from '@blerglab/chat'
import { ENGINE_LABELS, RUNTIME_LABELS } from './runtimes'

/** An engine by its product's name ("Claude Code"), as a model reads in full. */
const ENGINE_PRODUCT: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', hermes: 'Hermes', openclaw: 'OpenClaw' }

export function engineName(engine: string | undefined): string {
  const id = engine || 'claude'
  return ENGINE_PRODUCT[id] ?? ENGINE_LABELS[id] ?? id
}

/** A model's name in full: "claude-opus-5-5" is "Opus 5.5" (a date suffix dropped); any other
 *  id as the chat names it. */
export function modelFullName(id: string): string {
  const m = /^(?:[\w.-]+\/)?claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$/i.exec(id)
  if (m) return `${m[1].charAt(0).toUpperCase()}${m[1].slice(1).toLowerCase()} ${m[2]}${m[3] ? `.${m[3]}` : ''}`
  return modelDisplayName(id)
}

/** "Claude Code · Opus 5.5" (the raw id in the tooltip); the engine's default when none is set. */
export function modelFact(engine: string | undefined, model: string | undefined): { value: string; raw?: string } {
  const name = engineName(engine)
  if (!model) return { value: `${name} · engine default` }
  return { value: `${name} · ${modelFullName(model)}`, raw: model }
}

/** A subagent's model, always stated (subagents can run on other models than Main): its own
 *  ("Sonnet 5.5"), else the one it inherits from the session ("Opus 5.5 (same as Main)"), else, when
 *  even that is not known, "same as Main". The raw id goes in the tooltip. */
export function subagentModelFact(own: string | undefined, main: string | undefined): { value: string; raw?: string } {
  if (own) return { value: modelFullName(own), raw: own }
  if (main) return { value: `${modelFullName(main)} (same as Main)`, raw: main }
  return { value: 'same as Main' }
}

export interface RunFactsInput {
  title: string
  kind: string
  /** Absent: no engine (a shell command); the model then too. */
  engine?: string
  runtime?: string
  /** Absent: not known (left out); '' the engine's default. */
  model?: string
  /** The model fact as given (a subagent's, subagentModelFact), in place of `model`. */
  modelAs?: { value: string; raw?: string }
  state: string
  step?: string
  elapsed?: string
  tokens?: string
  card?: string
  startedBy?: string
}

export function runTarget(i: RunFactsInput): RunTarget {
  const runtime = i.runtime ? RUNTIME_LABELS[i.runtime] ?? i.runtime : ''
  const f = (v: string | undefined) => (v ? { value: v } : undefined)
  const facts: RunTarget['facts'] = {
    kind: { value: i.kind },
    engine: i.engine !== undefined ? f([engineName(i.engine), runtime].filter(Boolean).join(' · ')) : f(runtime),
    model: i.modelAs ?? (i.engine !== undefined && i.model !== undefined ? modelFact(i.engine, i.model) : undefined),
    state: { value: i.state },
    step: f(i.step),
    elapsed: f(i.elapsed),
    tokens: f(i.tokens),
    card: f(i.card),
    startedBy: f(i.startedBy),
  }
  return { kind: 'run', title: i.title, facts }
}
