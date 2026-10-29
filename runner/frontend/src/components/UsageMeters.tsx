import { useEffect, useState } from 'react'
import type { DaemonInfo, UsageLimits, UsageWindow } from '../types'
import { clockTime, meterLevel, resetsIn } from '../lib/usageLimits'
import './UsageMeters.css'

// The providers' usage limits, as the workstation daemons report them: one compact row per
// provider under the "Where sessions run" pills, a thin bar per window, its tooltip the share used
// and left and when it resets. Limits belong to the provider account a machine is logged in to, so
// a row names the machine only when more than one reports. A provider a connected machine can run
// that has not reported yet gets a quiet placeholder row ("…") whose tooltip says what to expect;
// one whose account has no window says that ("—"), and one this daemon does not read says that
// ("⊘", with why it may be). Only shares and reset times reach the
// browser (the daemon sends no amount), so no money is shown.

const PROVIDER_NAMES: Record<string, string> = { codex: 'Codex', claude: 'Claude' }
const WINDOW_LABELS: Record<string, string> = { '5h': '5h', '7d': 'week', '7d_opus': 'Opus', '7d_sonnet': 'Sonnet', '7d_fable': 'Fable', month: 'month', day: 'day', 'spend-week': 'spend wk', spend: 'spend' }
const WINDOW_TITLES: Record<string, string> = {
  '5h': '5-hour window',
  '7d': 'Weekly window',
  '7d_opus': 'Opus weekly window',
  '7d_sonnet': 'Sonnet weekly window',
  '7d_fable': 'Fable weekly window',
  month: 'Monthly spend limit',
  day: 'Daily spend limit',
  'spend-week': 'Weekly spend limit',
  spend: 'Spend limit',
}
const WINDOW_ORDER = ['5h', '7d', '7d_opus', '7d_sonnet', '7d_fable', 'month', 'spend', 'day', 'spend-week', 'capped']
// The providers whose limits a daemon reads (internal/daemon/limits.go).
const METER_PROVIDERS = ['claude', 'codex']

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

/** The share left, as the bar's text would say it: "58", or "<1" when under one percent is left. */
function leftText(used: number): string {
  const left = 100 - Math.min(100, Math.max(0, used))
  if (left > 0 && left < 1) return '<1'
  return String(Math.round(left))
}

/** A window in words: "5-hour window: 42% used, 58% left · resets 14:00 (in 2h 10m)". */
function windowSummary(w: UsageWindow, now: Date, name = WINDOW_TITLES[w.id] ?? WINDOW_LABELS[w.id] ?? w.id): string {
  const underOne = w.used_percent > 0 && w.used_percent < 1
  const used = underOne ? '<1' : String(Math.round(Math.min(100, Math.max(0, w.used_percent))))
  const when = resetsIn(w.resets_at, now)
  return `${name}: ${used}% used, ${leftText(w.used_percent)}% left${when && w.resets_at ? ` · resets ${resetClock(w.resets_at, now)} (${when})` : ''}`
}

function Window({ provider, w, now, fetchedAt }: { provider: string; w: UsageWindow; now: Date; fetchedAt?: string }) {
  const when = resetsIn(w.resets_at, now)
  const asOf = fetchedAt && !Number.isNaN(new Date(fetchedAt).getTime()) ? clockTime(new Date(fetchedAt)) : ''
  if (w.id === 'capped') {
    return (
      <span
        data-testid="usage-window-capped"
        role="status"
        className="usage-meter usage-meter--danger usage-meter--capped usage-meter--pulse"
        title={when && w.resets_at ? `${providerName(provider)} refused a call for its usage limit · resets ${resetClock(w.resets_at, now)} (${when})` : `${providerName(provider)} usage limit reached · reset time unknown`}
      >
        <span className="usage-meter__dot" />
        <span className="usage-meter__pct">capped{w.resets_at ? ` · resets ${resetClock(w.resets_at, now)}` : ''}</span>
        <span className="usage-meter__critical" role="img" aria-label="Usage limit reached">!</span>
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
  const title = windowSummary(w, now, name) + (asOf ? ` · as of ${asOf}` : '')
  return (
    <span
      data-testid={`usage-window-${w.id}`}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={underOne ? Math.round(w.used_percent * 10) / 10 : pct}
      aria-label={`${providerName(provider)} ${name.toLowerCase()}, ${spoken}% used, ${leftText(w.used_percent).replace('<1', 'under 1')}% left${when ? `, resets ${when}` : ''}${asOf ? `, as of ${asOf}` : ''}${pct >= 95 ? ', usage limit nearly reached' : ''}`}
      className={`usage-meter usage-meter--${meterLevel(pct)}${pct >= 95 ? ' usage-meter--pulse' : ''}`}
      title={title}
    >
      <span className="usage-meter__label" aria-hidden="true">{label}</span>
      <span className="usage-meter__track" aria-hidden="true"><span className="usage-meter__fill" style={{ width: `${underOne ? 2 : pct}%` }} /></span>
      <span className="usage-meter__pct" aria-hidden="true">{pctText}%</span>
      {pct >= 95 && <span className="usage-meter__critical" role="img" aria-label="Usage limit nearly reached">!</span>}
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

  const runs = (d: DaemonInfo, p: string) => (d.available_engines ?? []).includes(p)
  // A row per (machine, provider) with windows, or a note: no window on the account, or (for an
  // engine the machine runs) not read by this daemon.
  const rows: Row[] = daemons.flatMap(d => (d.usage_limits ?? [])
    .map(u => ({ daemon: d, provider: u.provider, fetchedAt: u.fetched_at, windows: liveWindows(u, now), none: u.note === 'no_windows', notRead: u.note === 'not_read' && runs(d, u.provider), expired: (u.windows ?? []).length > 0 }))
    .filter(r => r.windows.length > 0 || r.none || r.notRead))
  // A provider a connected machine runs with nothing to draw: a placeholder until a reading comes
  // (or because the last reading's windows have all reset).
  const shown = new Set(rows.map(r => `${r.daemon.id}:${r.provider}`))
  const pending: Row[] = daemons
    .filter(d => d.status === 'connected')
    .flatMap(d => METER_PROVIDERS
      .filter(p => runs(d, p) && !shown.has(`${d.id}:${p}`) && !(d.usage_limits ?? []).some(u => u.provider === p && u.note === 'not_read'))
      .map(p => ({ daemon: d, provider: p, windows: [], pending: true, expired: (d.usage_limits ?? []).some(u => u.provider === p && (u.windows ?? []).length > 0) })))
  const all = [...rows, ...pending]
    .sort((a, b) => a.provider.localeCompare(b.provider) || a.daemon.name.localeCompare(b.daemon.name))
  if (all.length === 0) return null
  const nameMachines = new Set(all.map(r => r.daemon.id)).size > 1

  return (
    <div className="usage-meters" data-testid="usage-meters">
      {all.map(r => {
        const who = `${providerName(r.provider)}${nameMachines ? ` · ${r.daemon.name}` : ''}`
        // The row's own tooltip: every window in words, for a hover anywhere on the row.
        const engine = providerName(r.provider)
        const summary = r.pending
          ? r.expired
            ? `${engine}: the last reading's windows have all reset. The daemon's next read (within 10 minutes, or a few seconds after a ${engine} turn) brings the new ones.`
            : `${engine}: no usage limits from this machine yet. A daemon from 0.4.5 on reads them when it connects, every 10 minutes and a few seconds after a ${engine} turn (no session needed), and says here if it cannot read them. An older daemon never fills this row: update it, or see its log ("usage limits: ${r.provider}: …").`
          : r.notRead
            ? `${engine}: this machine's daemon does not read its usage limits: turned off (BLERG_RUNNER_CLAUDE_USAGE=0 for Claude), or the engine's sign-in is not one it can read (an API key, a token in the daemon's environment, or a macOS login kept in the Keychain). The daemon's log says which.`
            : r.windows.length === 0
              ? `${engine} answered that the account this machine is signed in to has no usage-limit window to show (an API key, or a usage-based work plan without a spend limit).`
              : `${who} · ${r.windows.map(w => w.id === 'capped' ? `capped${w.resets_at ? ` until ${resetClock(w.resets_at, now)}` : ''}` : windowSummary(w, now)).join(' · ')}`
        return (
          <div key={`${r.daemon.id}:${r.provider}`} className="usage-meters__row" data-testid={`usage-row-${r.provider}`} title={summary}>
            <span className="usage-meters__provider">{who}</span>
            <span className="usage-meters__windows">
              {r.pending
                ? <span className="usage-meters__empty" data-testid="usage-pending" role="img" aria-label={`${providerName(r.provider)}: no usage limits read yet`}>…</span>
                : r.notRead
                  ? <span className="usage-meters__empty" data-testid="usage-not-read" role="img" aria-label={`${engine}: usage limits not read on this machine`}>⊘</span>
                : r.windows.length === 0
                  ? <span className="usage-meters__empty" data-testid="usage-none" role="img" aria-label={`${providerName(r.provider)}: this account has no usage-limit window`}>—</span>
                  : r.windows.map(w => <Window key={w.id} provider={r.provider} w={w} now={now} fetchedAt={r.fetchedAt} />)}
            </span>
          </div>
        )
      })}
    </div>
  )
}

interface Row { daemon: DaemonInfo; provider: string; fetchedAt?: string; windows: UsageWindow[]; none?: boolean; notRead?: boolean; pending?: boolean; expired?: boolean }
