// BLG-471 §2: our own small parser for the declared gantt subset — `parseGantt` is also the
// classifier, same discipline as blergFlow/parse.ts and blergSequence/parse.ts: `null` for
// anything outside the subset, never a throw (an agent's own diagram text is untrusted input, not
// a program bug). A line only ever succeeds by being one of the productions below; `excludes`,
// `todayMarker`, `click`, `%%{init}%%`'s own *configuration* (treated as a plain comment, same gap
// as blergFlow/blergSequence's parsers today — not special-cased, matching the shipped
// precedent), a dateFormat other than `YYYY-MM-DD`, a task line that isn't fully consumed by the
// tag/id/start/duration grammar below, or a dependency on an id not yet declared, falls the whole
// diagram back to Mermaid — "unknown, not just rejected", the same rule every other parser here
// states in its own header.
import type { GanttGraph, GanttTask } from './ast'
import { decodeLabel } from '../blergFlow/label'

function skipFrontmatter(lines: readonly string[]): number {
  if (!/^\s*---\s*$/.test(lines[0] ?? '')) return 0
  const close = lines.findIndex((line, index) => index > 0 && /^-{3}\s*$/.test(line))
  return close < 0 ? -1 : close + 1
}
const isBlankOrComment = (line: string) => line.trim() === '' || /^\s*%%/.test(line)

const TITLE = /^title\s+(.*)$/i
const DATE_FORMAT = /^dateFormat\s+(\S+)$/i
const AXIS_FORMAT = /^axisFormat\s+(.*)$/i
const SECTION = /^section\s+(.*)$/i
const TAG = /^(done|active|crit|milestone)$/i
const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/
const DURATION = /^(\d+(?:\.\d+)?)(ms|s|m|h|d|w)$/i
const AFTER = /^after\s+(\S+(?:\s+\S+)*)$/i
const TASK_ID = /^[A-Za-z_][A-Za-z0-9_-]*$/

const MS_PER_UNIT: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 7 * 86_400_000 }

function parseIsoDate(value: string): Date | null {
  const match = ISO_DATE.exec(value)
  if (!match) return null
  const [, y, m, d] = match
  const date = new Date(Date.UTC(Number(y), Number(m) - 1, Number(d)))
  // Rejects an overflowed calendar date (e.g. 2025-02-30) rather than silently normalizing it —
  // Date.UTC wraps those into the following month, which would draw a task at the wrong time.
  if (date.getUTCFullYear() !== Number(y) || date.getUTCMonth() !== Number(m) - 1 || date.getUTCDate() !== Number(d)) return null
  return date
}

function addDuration(start: Date, amount: number, unit: string): Date {
  return new Date(start.getTime() + amount * MS_PER_UNIT[unit.toLowerCase()])
}

/** `null`: the task line isn't fully consumed by this grammar — the failure rule, falls back. */
function parseTask(line: string, section: string, index: number, tasksById: ReadonlyMap<string, GanttTask>): GanttTask | null {
  const colon = line.indexOf(':')
  if (colon < 0) return null
  const rawLabel = line.slice(0, colon).trim()
  const label = decodeLabel(rawLabel)
  if (label === null || label === '') return null
  const tokens = line.slice(colon + 1).split(',').map(token => token.trim())
  if (tokens.some(token => token === '')) return null

  const tags = new Set<string>()
  const rest: string[] = []
  for (const token of tokens) {
    const tag = TAG.exec(token)
    if (tag) tags.add(tag[1].toLowerCase())
    else rest.push(token)
  }

  let id: string
  let startSpec: string
  let durSpec: string
  if (rest.length === 3) [id, startSpec, durSpec] = rest
  else if (rest.length === 2) { id = `task-${index}`; [startSpec, durSpec] = rest }
  else return null
  if (!TASK_ID.test(id) || tasksById.has(id)) return null

  let start: Date
  let dependsOn: readonly string[] = []
  const after = AFTER.exec(startSpec)
  if (after) {
    const ids = after[1].split(/\s+/)
    const deps = ids.map(depId => tasksById.get(depId))
    if (deps.some(dep => !dep)) return null // a dependency not yet declared — unknown, falls back
    start = new Date(Math.max(...(deps as GanttTask[]).map(dep => dep.end.getTime())))
    dependsOn = ids
  } else {
    const literal = parseIsoDate(startSpec)
    if (!literal) return null
    start = literal
  }

  let end: Date
  const durMatch = DURATION.exec(durSpec)
  if (durMatch) end = addDuration(start, Number(durMatch[1]), durMatch[2])
  else {
    const literalEnd = parseIsoDate(durSpec)
    if (!literalEnd || literalEnd.getTime() < start.getTime()) return null
    end = literalEnd
  }

  return {
    id, label, section, start, end,
    milestone: tags.has('milestone'), crit: tags.has('crit'), active: tags.has('active'), done: tags.has('done'),
    dependsOn,
  }
}

/** `null`: not our declared subset (or not a gantt diagram at all) — the caller falls back to
 *  Mermaid, same contract as `parseFlowchart`/`parseSequence`. */
export function parseGantt(source: string): GanttGraph | null {
  const lines = source.split('\n')
  let i = skipFrontmatter(lines)
  if (i < 0) return null
  while (i < lines.length && isBlankOrComment(lines[i])) i++
  if (!/^\s*gantt\s*$/i.test(lines[i] ?? '')) return null
  i++

  let title: string | null = null
  let axisFormat: string | null = null
  const sections: string[] = []
  let currentSection = ''
  const tasksById = new Map<string, GanttTask>()
  const tasks: GanttTask[] = []

  for (; i < lines.length; i++) {
    const line = lines[i].trim()
    if (isBlankOrComment(line)) continue

    const titleMatch = TITLE.exec(line)
    if (titleMatch) {
      const decoded = decodeLabel(titleMatch[1].trim())
      if (decoded === null) return null
      title = decoded
      continue
    }
    const dateFormatMatch = DATE_FORMAT.exec(line)
    if (dateFormatMatch) {
      // The declared subset supports exactly one calendar form — the others are a different task
      // grammar entirely (design doc's own "not implied here" precision), not approximated.
      if (dateFormatMatch[1] !== 'YYYY-MM-DD') return null
      continue
    }
    const axisFormatMatch = AXIS_FORMAT.exec(line)
    if (axisFormatMatch) { axisFormat = axisFormatMatch[1].trim(); continue }
    const sectionMatch = SECTION.exec(line)
    if (sectionMatch) {
      const decoded = decodeLabel(sectionMatch[1].trim())
      if (decoded === null || decoded === '') return null
      currentSection = decoded
      if (!sections.includes(currentSection)) sections.push(currentSection)
      continue
    }

    const task = parseTask(line, currentSection, tasks.length, tasksById)
    if (!task) return null
    tasksById.set(task.id, task)
    tasks.push(task)
    if (!sections.includes(task.section)) sections.push(task.section)
  }

  if (tasks.length === 0) return null
  return { title, axisFormat, sections, tasks }
}
