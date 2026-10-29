import { useState, type ReactNode } from 'react'
import { WRAP_UNAVAILABLE, useLineWrap, type WrapKind } from '../model/lineWrap'

/** The embedded viewer's only chrome. Hosts can reuse it beside future split panes. */
export default function ViewerToolbar({ titleId, title, meta, copyValue = title, copyLabel = 'Copy path', toggle, children }: {
  titleId: string
  title: string
  meta: string
  copyValue?: string
  copyLabel?: string
  toggle?: ReactNode
  children?: ReactNode
}) {
  const [copied, setCopied] = useState(false)
  function copy() {
    void navigator.clipboard?.writeText(copyValue).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    }, () => {})
  }
  return <div className="viewer-toolbar" role="toolbar" aria-label="File actions">
    <h2 id={titleId}><button type="button" className="viewer-path" title={`${title} · ${meta}`} aria-label={`Copy ${title} ${copyLabel === 'Copy sha' ? 'sha' : 'path'}`} onClick={copy}>{title}</button></h2>
    {toggle}
    <ViewerAction icon="copy" label={copyLabel} onClick={copy} />
    {children}
    <span className="chat-sr-only" role="status">{copied ? 'Copied' : ''}</span>
  </div>
}

const paths = {
  copy: 'M9 9h11v11H9zM5 15H3V3h12v2',
  download: 'M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5',
  save: 'M12 5v14m-7-7h14M3 3h18v18H3z',
  review: 'M4 3h16v13H9l-5 5zM8 7h8M8 11h6',
  markup: 'm4 16 12-12 4 4L8 20H4zM13 7l4 4',
  back: 'm10 5-7 7 7 7M3 12h18',
  wrap: 'M3 6h18M3 12h15a3 3 0 0 1 0 6h-5m0 0 2-2m-2 2 2 2M3 18h6',
}
export function ViewerAction({ icon, label, onClick, disabled = false, pressed, title = label, wrapToggle }: {
  icon: keyof typeof paths
  label: string
  onClick: () => void
  disabled?: boolean
  pressed?: boolean
  title?: string
  wrapToggle?: boolean
}) {
  return <button type="button" className="viewer-action" aria-label={label} title={title} aria-pressed={pressed} data-wrap-toggle={wrapToggle || undefined} disabled={disabled} onClick={onClick}>
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[icon]} /></svg>
  </button>
}

/** The line wrap toggle (#265): pressed while lines wrap; Alt+Z and the palette click it. An icon in
 *  a docked viewer's toolbar, a text button in a modal header or the diff's toolbar. */
export function WrapToggle({ kind, text = false, available = true }: { kind: WrapKind; text?: boolean; available?: boolean }) {
  const [saved, toggle] = useLineWrap(kind)
  const on = saved && available
  const title = available ? 'Wrap lines (Alt+Z)' : WRAP_UNAVAILABLE
  if (text) return <button type="button" className="artifact-btn" aria-pressed={on} title={title} disabled={!available} data-wrap-toggle onClick={toggle}>Wrap</button>
  return <ViewerAction icon="wrap" label="Wrap lines" title={title} pressed={on} disabled={!available} wrapToggle onClick={toggle} />
}
