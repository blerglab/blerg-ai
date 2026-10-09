// Human time labels for transcript cards and message lists. Pure functions:
// every one takes `now` (and optionally a locale and time zone) so results are
// deterministic in tests. Built on Intl only — the person's locale decides
// 12h/24h and month/weekday names.

export interface TimeOpts {
  /** BCP 47 locale; default: the browser's. */
  locale?: string
  /** IANA zone; default: the browser's. */
  timeZone?: string
}

const MINUTE = 60_000
const HOUR = 3_600_000
const DAY = 86_400_000

/** The shared tick resolution: labels are recomputed at most this often. */
export const TICK_MS = 30_000

/** parseTs: epoch ms for an ISO-ish string or number; null when missing or unparsable. */
export function parseTs(ts: string | number | null | undefined): number | null {
  if (ts === null || ts === undefined || ts === '') return null
  const t = typeof ts === 'number' ? ts : Date.parse(ts)
  return Number.isFinite(t) ? t : null
}

// Intl formatters are costly to build and a long transcript asks for
// thousands of labels: build each shape once.
const dtfCache = new Map<string, Intl.DateTimeFormat>()
const rtfCache = new Map<string, Intl.RelativeTimeFormat>()

function dtf(o: TimeOpts, options: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = `${o.locale ?? ''}|${o.timeZone ?? ''}|${JSON.stringify(options)}`
  let f = dtfCache.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat(o.locale, { ...options, timeZone: o.timeZone })
    dtfCache.set(key, f)
  }
  return f
}

function rtf(o: TimeOpts, style: 'short' | 'long', numeric: 'always' | 'auto'): Intl.RelativeTimeFormat {
  const key = `${o.locale ?? ''}|${style}|${numeric}`
  let f = rtfCache.get(key)
  if (!f) {
    f = new Intl.RelativeTimeFormat(o.locale, { style, numeric })
    rtfCache.set(key, f)
  }
  return f
}

function isEnglish(o: TimeOpts): boolean {
  const loc = o.locale ?? (typeof navigator !== 'undefined' ? navigator.language : 'en')
  return !loc || loc.toLowerCase().startsWith('en')
}

// Calendar day number (days since epoch of the local date) in the zone.
// Comparing these — not millisecond differences — keeps "yesterday" right
// across DST changes, when a day is 23 or 25 hours long.
function dayNumber(ms: number, o: TimeOpts): number {
  let y = 1970
  let m = 1
  let d = 1
  for (const p of dtf(o, { year: 'numeric', month: 'numeric', day: 'numeric' }).formatToParts(ms)) {
    if (p.type === 'year') y = Number(p.value)
    else if (p.type === 'month') m = Number(p.value)
    else if (p.type === 'day') d = Number(p.value)
  }
  return Math.round(Date.UTC(y, m - 1, d) / DAY)
}

/** dayKey: a stable id for the local calendar date of ms, for change detection. */
export function dayKey(ms: number, o: TimeOpts = {}): number {
  return dayNumber(ms, o)
}

const timeOnly = { hour: 'numeric', minute: '2-digit' } as const

// Intl's short unit reads "4 min. ago"; drop the abbreviation dot.
function rel(o: TimeOpts, n: number, unit: Intl.RelativeTimeFormatUnit): string {
  return rtf(o, 'short', 'always').format(-n, unit).replace(/\./g, '')
}

function sameYear(a: number, b: number, o: TimeOpts): boolean {
  const f = dtf(o, { year: 'numeric' })
  return f.format(a) === f.format(b)
}

/**
 * relativeLabel: how long before `now` ms happened — "just now", "4 min ago",
 * "2 hr ago" within a day, then "yesterday 2:32 PM", "Mon 2:32 PM" within the
 * last week, else the date. A timestamp ahead of `now` (clock skew) reads as
 * "just now". Returns '' for an unusable input.
 */
export function relativeLabel(ms: number, now: number, o: TimeOpts = {}): string {
  if (!Number.isFinite(ms) || !Number.isFinite(now)) return ''
  const age = Math.max(0, now - ms)
  if (age < MINUTE) return isEnglish(o) ? 'just now' : rtf(o, 'long', 'auto').format(0, 'second')
  if (age < HOUR) return rel(o, Math.floor(age / MINUTE), 'minute')
  if (age < DAY) return rel(o, Math.floor(age / HOUR), 'hour')
  const daysAgo = dayNumber(now, o) - dayNumber(ms, o)
  const time = dtf(o, timeOnly).format(ms)
  if (daysAgo <= 1) return `${rtf(o, 'long', 'auto').format(-1, 'day')} ${time}`
  if (daysAgo < 7) return `${dtf(o, { weekday: 'short' }).format(ms)} ${time}`
  const date = dtf(o, sameYear(ms, now, o)
    ? { day: 'numeric', month: 'short' }
    : { day: 'numeric', month: 'short', year: 'numeric' }).format(ms)
  return `${date} ${time}`
}

/** absoluteLabel: the full local date and time, for a tooltip. */
export function absoluteLabel(ms: number, o: TimeOpts = {}): string {
  if (!Number.isFinite(ms)) return ''
  return dtf(o, { dateStyle: 'full', timeStyle: 'medium' }).format(ms)
}

function capitalize(s: string): string {
  return s ? s.charAt(0).toLocaleUpperCase() + s.slice(1) : s
}

/** dayLabel: a divider caption — "Today", "Yesterday", else "Mon, 28 Sep" (with the year when not this one). */
export function dayLabel(ms: number, now: number, o: TimeOpts = {}): string {
  if (!Number.isFinite(ms) || !Number.isFinite(now)) return ''
  const daysAgo = dayNumber(now, o) - dayNumber(ms, o)
  if (daysAgo <= 0) return isEnglish(o) ? 'Today' : capitalize(rtf(o, 'long', 'auto').format(0, 'day'))
  if (daysAgo === 1) return capitalize(rtf(o, 'long', 'auto').format(-1, 'day'))
  return dtf(o, sameYear(ms, now, o)
    ? { weekday: 'short', day: 'numeric', month: 'short' }
    : { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }).format(ms)
}

/** formatElapsed renders a duration as m:ss (h:mm:ss past an hour). */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

/** turnDuration: "took 42 s", "took 3 min 5 s", "took 1 h 2 min"; '' when not a sane span. */
export function turnDuration(startMs: number | null, endMs: number | null): string {
  if (startMs === null || endMs === null || endMs < startMs) return ''
  const total = Math.round((endMs - startMs) / 1000)
  if (total < 60) return `took ${total} s`
  const min = Math.floor(total / 60)
  const s = total % 60
  if (min < 60) return s ? `took ${min} min ${s} s` : `took ${min} min`
  const h = Math.floor(min / 60)
  const m = min % 60
  return m ? `took ${h} h ${m} min` : `took ${h} h`
}
