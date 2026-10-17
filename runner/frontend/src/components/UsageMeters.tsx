import { useEffect, useState } from 'react'
import type { DaemonInfo, UsageLimits, UsageWindow } from '../types'
import { clockTime, meterLevel, resetsIn } from '../lib/usageLimits'
import './UsageMeters.css'

// The providers' usage limits, as the workstation daemons report them: one compact row per
// provider under the "Where sessions run" pills, a thin bar per window. Limits belong to the
// provider account a machine is logged in to, so a row names the machine only when more than one
// reports. Nothing is drawn when nothing reports.

const PROVIDER_NAMES: Record<string, string> = { codex: 'Codex', claude: 'Claude' }
const WINDOW_LABELS: Record<string, string> = { '5h': '5h', '7d': 'week', month: 'month', day: 'day', 'spend-week': 'spend wk' }
const WINDOW_TITLES: Record<string, string> = {
  '5h': '5-hour window',
  '7d': 'Weekly window',
  month: 'Monthly spend limit',
  day: 'Daily spend limit',
  'spend-week': 'Weekly spend limit',
}
const WINDOW_ORDER = ['5h', '7d', 'month', 'day', 'spend-week', 'capped']

/** The clock time a cap lifts: "14:00", or "Mon 14:00" when that is not within the day. */
function resetClock(resetsAt: string, now: Date): string {
  const t = new Date(resetsAt)
  const clock = clockTime(t)
  if (t.getTime() - now.getTime() < 20 * 3600_000) return clock
  return `${t.toLocaleDateString([], { weekday: 'short' })} ${clock}`
}

function providerName(p: string): string {
  return PROVIDER_NAMES[p] ?? p
}

/** The windows still worth showing, in a fixed order: a window whose reset has passed is gone (its
 *  percentage is no longer true; the daemon's next read brings the new one), and so is a cap. A
 *  window with no reset time (a spend limit, which resets on the provider's billing cycle, or a
 *  reached spend limit) stays until the daemon's next read says otherwise. */
function liveWindows(u: UsageLimits, now: Date): UsageWindow[] {
  return (u.windows ?? [])
    .filter(w => !w.resets_at || new Date(w.resets_at).getTime() > now.getTime())
    .sort((a, b) => WINDOW_ORDER.indexOf(a.id) - WINDOW_ORDER.indexOf(b.id))
}

function Window({ provider, w, now, fetchedAt }: { provider: string; w: UsageWindow; now: Date; fetchedAt?: string }) {
  const when = resetsIn(w.resets_at, now)
  const asOf = fetchedAt && !Number.isNaN(new Date(fetchedAt).getTime()) ? clockTime(new Date(fetchedAt)) : ''
  if (w.id === 'capped') {
    return (
      <span
        data-testid="usage-window-capped"
        role="status"
        className="usage-meter usage-meter--danger usage-meter--capped"
        title={when ? `${providerName(provider)} refused a call for its usage limit · resets ${when}` : `${providerName(provider)} usage limit reached · reset time unknown`}
      >
        <span className="usage-meter__dot" />
        capped{w.resets_at ? ` · resets ${resetClock(w.resets_at, now)}` : ''}
      </span>
    )
  }
  const pct = Math.round(Math.min(100, Math.max(0, w.used_percent)))
  // Between 0 and 1 % is "<1%", not 0 %: some of the limit is used (a work account's first dollars).
  const underOne = w.used_percent > 0 && w.used_percent < 1
  const pctText = underOne ? '<1' : String(pct)
  const spoken = underOne ? 'under 1' : String(pct)
  const label = WINDOW_LABELS[w.id] ?? w.id
  const name = WINDOW_TITLES[w.id] ?? label
  return (
    <span
      data-testid={`usage-window-${w.id}`}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={underOne ? Math.round(w.used_percent * 10) / 10 : pct}
      aria-label={`${providerName(provider)} ${name.toLowerCase()}, ${spoken}% used${when ? `, resets ${when}` : ''}${asOf ? `, as of ${asOf}` : ''}`}
      className={`usage-meter usage-meter--${meterLevel(w.used_percent)}`}
      title={`${name}: ${pctText}% used${when ? ` · resets ${when}` : ''}${asOf ? ` · as of ${asOf}` : ''}`}
    >
      <span className="usage-meter__label" aria-hidden="true">{label}</span>
      <span className="usage-meter__track" aria-hidden="true"><span className="usage-meter__fill" style={{ width: `${underOne ? 2 : pct}%` }} /></span>
      <span className="usage-meter__pct" aria-hidden="true">{pctText}%</span>
      {/* For a screen with no hover (the phone): the reset as text, its largest unit ("↻2h"; the
          spoken name has it in full). UsageMeters.css shows it there only. */}
      {when && <span className="usage-meter__reset" aria-hidden="true">↻{when.replace(/^in /, '').split(' ')[0]}</span>}
    </span>
  )
}

export default function UsageMeters({ daemons, now: fixedNow }: { daemons: DaemonInfo[]; now?: Date }) {
  // Reset times move on their own: re-read the clock now and then (a cap lifts with no message).
  const [tick, setTick] = useState(() => new Date())
  useEffect(() => {
    if (fixedNow) return
    const id = setInterval(() => setTick(new Date()), 30_000)
    return () => clearInterval(id)
  }, [fixedNow])
  const now = fixedNow ?? tick

  const reporting = daemons.filter(d => (d.usage_limits ?? []).some(u => liveWindows(u, now).length > 0))
  const rows = reporting.flatMap(d => (d.usage_limits ?? [])
    .map(u => ({ daemon: d, provider: u.provider, fetchedAt: u.fetched_at, windows: liveWindows(u, now) }))
    .filter(r => r.windows.length > 0))
    .sort((a, b) => a.provider.localeCompare(b.provider) || a.daemon.name.localeCompare(b.daemon.name))
  if (rows.length === 0) return null
  const nameMachines = reporting.length > 1

  return (
    <div className="usage-meters" data-testid="usage-meters">
      {rows.map(r => (
        <div key={`${r.daemon.id}:${r.provider}`} className="usage-meters__row" data-testid={`usage-row-${r.provider}`}>
          <span className="usage-meters__provider">
            {providerName(r.provider)}{nameMachines ? ` · ${r.daemon.name}` : ''}
          </span>
          <span className="usage-meters__windows">
            {r.windows.map(w => <Window key={w.id} provider={r.provider} w={w} now={now} fetchedAt={r.fetchedAt} />)}
          </span>
        </div>
      ))}
    </div>
  )
}
