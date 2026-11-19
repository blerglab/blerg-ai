import { printContent } from '../model/contentExport'
import './IconButton.css'
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { WRAP_UNAVAILABLE, useLineWrap, type WrapKind } from '../model/lineWrap'

/** The viewer's only chrome. Hosts can reuse it beside future split panes. */
export default function ViewerToolbar({ titleId, title, meta, copyValue = title, copyLabel = 'Copy path', showPath = true, toggle, primary, exports, children, print = true, printDisabled = false }: {
  print?: boolean
  printDisabled?: boolean
  titleId: string
  title: string
  meta: string
  copyValue?: string
  copyLabel?: string
  /** The tab already names the file; retain a visible title only for a useful path. */
  showPath?: boolean
  toggle?: ReactNode
  exports?: ReactNode
  primary?: ReactNode
  children?: ReactNode
}) {
  const root = useRef<HTMLDivElement>(null)
  const exportMenu = useRef<HTMLDivElement>(null)
  const [copied, setCopied] = useState(false)
  const [compact, setCompact] = useState(false)
  const [more, setMore] = useState(false)
  useEffect(() => {
    const node = root.current
    if (!node || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => setCompact(node.clientWidth > 0 && node.clientWidth < 600))
    observer.observe(node)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (!more) return
    const dismiss = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.querySelector('.viewer-exports')?.contains(event.target)) setMore(false)
    }
    document.addEventListener('pointerdown', dismiss)
    return () => document.removeEventListener('pointerdown', dismiss)
  }, [more])
  useLayoutEffect(() => {
    const menu = exportMenu.current
    if (!compact || !more || !menu) return
    const place = () => {
      menu.style.transform = ''
      const rect = menu.getBoundingClientRect()
      const shift = Math.max(8 - rect.left, Math.min(0, window.innerWidth - 8 - rect.right))
      if (shift) menu.style.transform = `translateX(${shift}px)`
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [compact, more])
  function copy() {
    void navigator.clipboard?.writeText(copyValue).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    }, () => {})
  }
  return <div ref={root} className="viewer-toolbar" role="toolbar" aria-label="File actions">
    {showPath ? <h2 id={titleId}><button type="button" className="viewer-path" title={`${title} · ${meta}`} aria-label={`Copy ${title} ${copyLabel === 'Copy sha' ? 'sha' : 'path'}`} onClick={copy}>{title}</button></h2> : <span id={titleId} className="chat-sr-only">{title}</span>}
    {toggle}
    <ViewerAction icon="copy" label={copyLabel} onClick={copy} />
    {primary}
    {(print || exports) && <div className="viewer-exports" onKeyDown={event => {
      if (event.key === 'Escape' && more) { event.preventDefault(); event.stopPropagation(); setMore(false); event.currentTarget.querySelector<HTMLButtonElement>('[aria-label="More"]')?.focus() }
    }}>
      {compact && <button type="button" className="refresh-control viewer-action" aria-label="More" title="More" aria-expanded={more} onClick={() => setMore(value => !value)}><svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.5" /><circle cx="12" cy="12" r="1.5" /><circle cx="19" cy="12" r="1.5" /></svg></button>}
      <div ref={exportMenu} className={compact ? 'viewer-export-menu' : 'viewer-export-row'} hidden={compact && !more}>
        {print && <PrintAction disabled={printDisabled} content={() => root.current?.closest('.artifact-viewer')?.querySelector<HTMLElement>('.artifact-comparison.is-active, .artifact-body:not([hidden])') ?? null} />}
        {exports}
      </div>
    </div>}
    {children}
    <span className="chat-sr-only" role="status">{copied ? 'Copied' : ''}</span>
  </div>
}

const paths = {
  print: 'M6 8V3h12v5M6 17H3V8h18v9h-3M6 14h12v7H6z',
  copy: 'M9 9h11v11H9zM5 15H3V3h12v2',
  download: 'M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5',
  downloadSvg: 'M12 3v9m-5-5 5 5 5-5M4 21l4-6h8l4 6z',
  downloadPng: 'M12 3v9m-5-5 5 5 5-5M4 16h4v4H4zM10 16h4v4h-4zM16 16h4v4h-4z',
  save: 'M12 5v14m-7-7h14M3 3h18v18H3z',
  review: 'M4 3h16v13H9l-5 5zM8 7h8M8 11h6',
  markup: 'm4 16 12-12 4 4L8 20H4zM13 7l4 4',
  back: 'm10 5-7 7 7 7M3 12h18',
  unified: 'M4 4h16v16H4zM4 9h16M4 14h16',
  split: 'M3 4h18v16H3zM12 4v16',
  zoomIn: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14m5 12 6 6M7 10h6m-3-3v6',
  zoomOut: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14m5 12 6 6M7 10h6',
  actualSize: 'M4 4h16v16H4zM10 9h2v6m-2 0h4',
  raw: 'm8 6-6 6 6 6m8-12 6 6-6 6m-3-15-2 18',
  follow: 'M4 8a8 8 0 0 1 14-2l3 3m0-6v6h-6M20 16a8 8 0 0 1-14 2l-3-3m0 6v-6h6',
  changes: 'M8 3v12m-4-4 4 4 4-4M16 21V9m-4 4 4-4 4 4',
  close: 'm6 6 12 12M6 18 18 6',
  maximize: 'M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5',
  restore: 'M8 3v5H3m13-5v5h5M3 16h5v5m13-5h-5v5',
  wrap: 'M3 6h18M3 12h15a3 3 0 0 1 0 6h-5m0 0 2-2m-2 2 2 2M3 18h6',
  popOut: 'M14 3h7v7m0-7-9 9M10 5H4v15h15v-6',
}
export function ViewerAction({ icon, label, onClick, disabled = false, pressed, title = label, wrapToggle, text }: {
  icon: keyof typeof paths
  label: string
  onClick: () => void
  disabled?: boolean
  pressed?: boolean
  title?: string
  wrapToggle?: boolean
  text?: string
}) {
  return <button type="button" className={`refresh-control viewer-action${text ? ' viewer-action-labeled' : ''}`} aria-label={label} title={title} aria-pressed={pressed} data-wrap-toggle={wrapToggle || undefined} disabled={disabled} onClick={onClick}>
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[icon]} /></svg>
    {text && <span>{text}</span>}
  </button>
}

/** The line wrap toggle (#265): pressed while lines wrap; Alt+Z and the palette click it. An icon in
 *  the shared utility bar, including modal and diff viewers. */
export function WrapToggle({ kind, available = true }: { kind: WrapKind; text?: boolean; available?: boolean }) {
  const [saved, toggle] = useLineWrap(kind)
  const on = saved && available
  const title = available ? 'Wrap lines (Alt+Z)' : WRAP_UNAVAILABLE
  return <ViewerAction icon="wrap" label="Wrap lines" title={title} pressed={on} disabled={!available} wrapToggle onClick={toggle} />
}

/** Nested renderers contribute their actions to the object's utility bar. */
export const ViewerActionsContext = createContext<HTMLElement | null | undefined>(undefined)
export function ViewerControls({ children, className = 'diff-toolbar', label }: { children: ReactNode; className?: string; label?: string }) {
  const host = useContext(ViewerActionsContext)
  return host === undefined ? <div className={className} role={label ? 'toolbar' : undefined} aria-label={label}>{children}</div> : host ? createPortal(children, host) : null
}

export function PrintAction({ content, disabled = false }: { content: () => HTMLElement | null; disabled?: boolean }) {
  const [error, setError] = useState('')
  return <>
    <ViewerAction icon="print" label="Print" disabled={disabled} onClick={() => {
      setError('')
      void printContent(content()).catch(() => {
        console.warn('content_print_failed', { cause: 'browser_print_rejected' })
        setError('Could not print this content. Try again.')
      })
    }} />
    {error && <span role="alert" className="artifact-error">{error}</span>}
  </>
}
