import { useContext, useMemo, type CSSProperties } from 'react'
import { useClockNow } from '../hooks/useSharedClock'
import { ClockOffsetContext } from '../model/clockOffset'
import { absoluteLabel, dayLabel, parseTs, relativeLabel } from '../model/timeLabel'

const DAY_MS = 86_400_000
const WEEK_AND_A_DAY = 8 * DAY_MS

interface Props {
  ts: string | number | null | undefined
  className?: string
  style?: CSSProperties
}

// TimeLabel: a relative time in a <time> element, absolute in its title.
// Renders nothing for a missing or unparsable timestamp. It shares one clock
// with every other label and is deliberately not a live region.
export function TimeLabel({ ts, className, style }: Props) {
  const ms = parseTs(ts)
  const offset = useContext(ClockOffsetContext)
  const now = useClockNow() - offset
  // Past a week a label is a fixed date: it only needs the clock daily.
  const clock = ms !== null && now - ms > WEEK_AND_A_DAY ? Math.floor(now / DAY_MS) * DAY_MS : now
  const label = useMemo(
    () => (ms === null ? '' : relativeLabel(ms, clock)),
    [ms, clock],
  )
  const abs = useMemo(() => (ms === null ? '' : absoluteLabel(ms)), [ms])
  if (ms === null || !label) return null
  return (
    <time
      className={`card-time${className ? ` ${className}` : ''}`}
      style={style}
      dateTime={new Date(ms).toISOString()}
      title={abs}
    >
      {label}
    </time>
  )
}

// DayDivider: a slim centered caption between days of a transcript.
export function DayDivider({ ts }: { ts: number }) {
  const offset = useContext(ClockOffsetContext)
  const now = useClockNow() - offset
  const label = useMemo(() => dayLabel(ts, now), [ts, now])
  return (
    <div className="day-divider" role="separator" data-testid="day-divider">
      <time dateTime={new Date(ts).toISOString()}>{label}</time>
    </div>
  )
}
