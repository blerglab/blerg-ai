// The schedule shapes the crons form offers as presets, and the plain-words description of a
// five-field expression. The server is the authority on what a schedule means and whether it is
// allowed (minimum interval, real dates); this only builds the common shapes and reads them back.

export type PresetKind = 'daily' | 'weekdays' | 'weekly'

export interface Preset {
  kind: PresetKind
  times: string[] // "HH:MM", 24 hour
  dow: number // 0 (Sunday) .. 6, used by 'weekly'
}

export const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const TIME = /^(\d{2}):(\d{2})$/
const pad = (n: number) => String(n).padStart(2, '0')

// buildExpression turns a preset into a five-field expression. Every time must share its minute:
// one expression cannot say "08:00 and 17:30", so that is refused rather than quietly changed.
export function buildExpression(p: Preset): { expr: string } | { error: string } {
  if (p.times.length === 0) return { error: 'Pick a time.' }
  const hours = new Set<number>()
  let minute = -1
  for (const t of p.times) {
    const m = TIME.exec(t)
    if (!m) return { error: 'Pick a time for every run.' }
    const h = Number(m[1])
    const mi = Number(m[2])
    if (h > 23 || mi > 59) return { error: 'Pick a valid time.' }
    if (minute >= 0 && mi !== minute) {
      return { error: 'All times in one schedule must share the same minute (for example 08:30 and 17:30). Use the advanced field for anything else.' }
    }
    minute = mi
    hours.add(h)
  }
  const dow = p.kind === 'daily' ? '*' : p.kind === 'weekdays' ? '1-5' : String(p.dow)
  return { expr: `${minute} ${[...hours].sort((a, b) => a - b).join(',')} * * ${dow}` }
}

const PRESET_RE = /^(\d{1,2}) (\d{1,2}(?:,\d{1,2})*) \* \* (\*|1-5|[0-6])$/

// parseExpression reads back an expression the presets could have built, or null.
export function parseExpression(expr: string): Preset | null {
  const m = PRESET_RE.exec(expr.trim().replace(/\s+/g, ' '))
  if (!m) return null
  const minute = Number(m[1])
  const hours = m[2].split(',').map(Number)
  if (minute > 59 || hours.some(h => h > 23)) return null
  const times = hours.map(h => `${pad(h)}:${pad(minute)}`)
  if (m[3] === '*') return { kind: 'daily', times, dow: 1 }
  if (m[3] === '1-5') return { kind: 'weekdays', times, dow: 1 }
  return { kind: 'weekly', times, dow: Number(m[3]) }
}

function joinTimes(times: string[]): string {
  if (times.length === 1) return times[0]
  return `${times.slice(0, -1).join(', ')} and ${times[times.length - 1]}`
}

// describeSchedule says a schedule in words; an expression that is not a preset shape is shown as is.
export function describeSchedule(expr: string): string {
  const p = parseExpression(expr)
  if (!p) return `Custom schedule: ${expr}`
  const at = ` at ${joinTimes(p.times)}`
  if (p.kind === 'daily') return p.times.length === 1 ? `Every day${at}` : `${p.times.length} times a day,${at}`
  if (p.kind === 'weekdays') return `Weekdays${at}`
  return `Every ${DAY_NAMES[p.dow]}${at}`
}

export function hasFiveFields(expr: string): boolean {
  const f = expr.trim().split(/\s+/)
  return f.length === 5 && f[0] !== ''
}
