// How the sidebar's usage-limit meters read a window (components/UsageMeters.tsx).

export type MeterLevel = 'quiet' | 'warning' | 'danger'

/** quiet below 80 %, warning from 80 %, danger from 90 %. */
export function meterLevel(percent: number): MeterLevel {
  if (percent >= 90) return 'danger'
  if (percent >= 80) return 'warning'
  return 'quiet'
}

/** "in 2h 10m", "in 3d 4h", "in 45m"; '' when the reset is unknown or has passed. */
export function resetsIn(resetsAt: string | undefined, now: Date): string {
  if (!resetsAt) return ''
  const mins = Math.floor((new Date(resetsAt).getTime() - now.getTime()) / 60_000)
  const ms = new Date(resetsAt).getTime() - now.getTime()
  if (Number.isNaN(ms) || ms <= 0) return '' // passed: no longer a time to wait for
  if (mins < 1) return 'in <1m'
  if (mins < 60) return `in ${mins}m`
  if (mins < 1440) return `in ${Math.floor(mins / 60)}h ${mins % 60}m`
  return `in ${Math.floor(mins / 1440)}d ${Math.floor((mins % 1440) / 60)}h`
}

/** A clock time on a 24-hour clock ("14:00", never "24:05"). */
export function clockTime(t: Date): string {
  return t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
}
