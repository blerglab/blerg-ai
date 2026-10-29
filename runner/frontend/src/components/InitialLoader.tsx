import { useEffect, useState, type ReactNode } from 'react'
import './InitialLoader.css'

type Shape = 'rows' | 'facts' | 'terminal'
type Props = {
  loading: boolean
  /** A cached result, including an empty result, always takes precedence over loading. */
  hasContent?: boolean
  active?: boolean
  loadKey?: string
  label: string
  shape?: Shape
  children?: ReactNode
}

/** Initial reads only: fast reads draw nothing, and refreshes keep the content already on screen. */
export default function InitialLoader({ loading, hasContent = false, active = true, loadKey, label, shape = 'rows', children }: Props) {
  if (!loading || hasContent) return children
  return active ? <DelayedSkeleton key={loadKey} label={label} shape={shape} /> : null
}

function DelayedSkeleton({ label, shape }: { label: string; shape: Shape }) {
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    const timer = setTimeout(() => setVisible(true), 150)
    return () => clearTimeout(timer)
  }, [])
  if (!visible) return null
  return <div className="initial-loader-region" role="region" aria-label={label} aria-busy="true">
    <span className="initial-loader-label" role="status">Loading {label}</span>
    <div className={`initial-skeleton initial-skeleton-${shape}`} aria-hidden="true">
      {Array.from({ length: shape === 'terminal' ? 7 : 5 }, (_, i) => <div className="initial-skeleton-row" key={i}>
        <span className="initial-skeleton-key" />
        <span className="initial-skeleton-value" />
      </div>)}
    </div>
  </div>
}
