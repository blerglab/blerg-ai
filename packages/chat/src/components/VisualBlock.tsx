import { useCallback, useEffect, useId, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { renderVisual } from '../visual/render'
import type { VisualKind } from '../visual/policy'
import { observeVisualTheme, readVisualTheme, type VisualTheme } from '../visual/theme'
import CodeBlock from './CodeBlock'
import './visual.css'

function Surface({kind, source, theme, zoom = 1, onError}: {
  kind: VisualKind; source: string; theme: VisualTheme; zoom?: number; onError: (reason: string) => void
}) {
  const target = useRef<HTMLDivElement>(null)
  useEffect(() => {
    let stopped = false
    let dispose: (() => void) | undefined
    const node = target.current
    if (!node) return
    let width = node.clientWidth
    let frame = 0
    const draw = () => {
      dispose?.()
      dispose = undefined
      // Each rendering owns a separate host, including late work after a resize.
      const host = document.createElement('div')
      node.replaceChildren(host)
      void renderVisual(kind, source, host, theme).then(cleanup => {
        if (stopped || host.parentElement !== node) cleanup()
        else dispose = cleanup
      }, (error: unknown) => { if (!stopped && host.parentElement === node) onError(failureReason(error)) })
    }
    draw()
    const resize = typeof ResizeObserver === 'undefined' || kind !== 'vega-lite' ? undefined : new ResizeObserver(() => {
      if (node.clientWidth === width) return
      width = node.clientWidth
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(draw)
    })
    resize?.observe(node)
    return () => { stopped = true; cancelAnimationFrame(frame); resize?.disconnect(); node.replaceChildren(); dispose?.() }
  }, [kind, source, theme, onError])
  return <div className="visual-surface" ref={target} style={{zoom}} />
}

function FullScreen({kind, source, theme, onClose, onError}: {
  kind: VisualKind; source: string; theme: VisualTheme; onClose: () => void; onError: (reason: string) => void
}) {
  const dialog = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const [zoom, setZoom] = useState(1)
  const backdrop = useBackdropClose(onClose)
  useEffect(() => {
    const previous = document.activeElement
    dialog.current?.focus()
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus() }
  }, [])
  // Portals escape the chat root. Carry its resolved tokens into the viewer.
  const style = {
    '--chat-surface': theme.surface, '--chat-surface-2': theme.surface2,
    '--chat-fg': theme.fg, '--chat-muted': theme.muted, '--chat-border': theme.border,
    '--chat-accent': theme.accent, '--chat-font': theme.fontFamily,
    '--chat-text-sm': `${theme.fontSize}px`,
  } as CSSProperties
  return createPortal(<div className="artifact-overlay artifact-overlay-viewer" style={style} {...backdrop}>
    <div className="artifact-dialog artifact-viewer visual-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}
      tabIndex={-1} ref={dialog} onKeyDown={event => {
        if (event.key === 'Escape') {event.stopPropagation(); onClose()}
        if (event.key === 'Tab') {
          const buttons = dialog.current?.querySelectorAll<HTMLButtonElement>('button')
          const first = buttons?.[0]
          const last = buttons?.[buttons.length - 1]
          if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
            event.preventDefault(); last?.focus()
          } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
            event.preventDefault(); first?.focus()
          }
        }
      }}>
      <header className="artifact-header"><h2 id={titleId}>{kind === 'mermaid' ? 'Mermaid diagram' : 'Vega-Lite chart'}</h2>
        <button type="button" onClick={() => setZoom(value => Math.max(0.25, value - 0.25))} aria-label="Zoom out">−</button>
        <button type="button" onClick={() => setZoom(1)} aria-label="Reset zoom">{Math.round(zoom * 100)}%</button>
        <button type="button" onClick={() => setZoom(value => Math.min(4, value + 0.25))} aria-label="Zoom in">+</button>
        <button type="button" className="artifact-close" onClick={onClose} aria-label="Close">✕</button>
      </header>
      <div className="visual-full-scroll"><Surface kind={kind} source={source} theme={theme} zoom={zoom} onError={onError} /></div>
    </div>
  </div>, document.body)
}

/** A short reason from a refusal or a renderer error ("line 12: url() not allowed"). */
function failureReason(error: unknown): string {
  const text = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  const line = text.split('\n').map(part => part.trim()).find(Boolean)
  if (!line) return 'the renderer failed'
  return line.length > 300 ? line.slice(0, 299) + '…' : line
}

export default function VisualBlock({kind, source}: {kind: VisualKind; source: string}) {
  // Remount state when source changes, so an incomplete/invalid earlier artifact cannot
  // poison a later valid revision and a previous successful rendering never looks current.
  return <VisualContent key={`${kind}:${source}`} kind={kind} source={source} />
}

function VisualContent({kind, source}: {kind: VisualKind; source: string}) {
  const root = useRef<HTMLElement>(null)
  const [theme, setTheme] = useState<VisualTheme>()
  useEffect(() => {
    const node = root.current
    if (!node) return
    setTheme(readVisualTheme(node))
    return observeVisualTheme(node, setTheme)
  }, [])
  const [failed, setFailed] = useState<string | null>(null)
  const [showSource, setShowSource] = useState(false)
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])
  // React state setters are stable, preventing renders from restarting chart initialization.
  const fail = useCallback((reason: string) => { setOpen(false); setFailed(reason) }, [])
  // Never silently: the source, with why it was not drawn.
  if (failed !== null) return <div className="visual-failed" data-testid="visual-failed">
    <p className="visual-failed-reason" role="status">{`Couldn’t draw this ${kind === 'mermaid' ? 'diagram' : 'chart'}: ${failed}`}</p>
    <CodeBlock closed>{source}</CodeBlock>
  </div>
  return <section ref={root} className="visual-block" aria-label={kind === 'mermaid' ? 'Mermaid preview' : 'Vega-Lite preview'}>
    <div className="visual-actions">
      <span>{kind === 'mermaid' ? 'Mermaid' : 'Vega-Lite'}</span>
      <button type="button" aria-pressed={showSource} onClick={() => setShowSource(value => !value)}>Source</button>
      {typeof navigator !== 'undefined' && navigator.clipboard && <button type="button" onClick={() => {
        void navigator.clipboard.writeText(source).then(() => setCopied(true), () => {})
      }}>{copied ? 'Copied' : 'Copy'}</button>}
      <button type="button" onClick={() => setOpen(true)}>Open</button>
    </div>
    {showSource ? <CodeBlock closed>{source}</CodeBlock> : theme && <Surface kind={kind} source={source} theme={theme} onError={fail} />}
    {open && theme && <FullScreen kind={kind} source={source} theme={theme} onClose={() => setOpen(false)} onError={fail} />}
  </section>
}

/** A diagram or chart drawn small, with nothing around it (a hovercard's preview): the same
 *  renderer and sandbox as the block (a Mermaid diagram in its sandboxed frame, CSP default-src
 *  'none'); a failure says so in one line. */
export function VisualThumb({kind, source}: {kind: VisualKind; source: string}) {
  const root = useRef<HTMLDivElement>(null)
  const [theme, setTheme] = useState<VisualTheme>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    const node = root.current
    if (!node) return
    setTheme(readVisualTheme(node))
    return observeVisualTheme(node, setTheme)
  }, [])
  const fail = useCallback(() => setFailed(true), [])
  return <div ref={root} className="visual-thumb" data-testid="visual-thumb" data-kind={kind}>
    {failed
      ? <p className="visual-failed-reason" role="status">{`Couldn’t draw this ${kind === 'mermaid' ? 'diagram' : 'chart'}.`}</p>
      : theme && <Surface kind={kind} source={source} theme={theme} onError={fail} />}
  </div>
}
