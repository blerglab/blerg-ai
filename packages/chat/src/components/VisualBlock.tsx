import { registerPrintVisual } from '../model/contentExport'
import { VisualExportActions } from './VisualExportActions'
import {supportsNativeDataUpdates} from '../visual/blerg/liveDataSupport'
import {sameDataStructure} from '../visual/blerg/dataIdentity'
import type {TransportFiles} from '../transport/types'
import { ViewerAction, ViewerActionsContext } from './ViewerToolbar'
import { useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useChipOpener } from './RefLinks'
import { ChipOverlay } from '../visual/ChipOverlay'
import { blergChartSize, getChartModel, renderVisual, type RenderedVisual, type VisualFit } from '../visual/render'
import { observeSurface } from '../visual/observeSurface'
import { offerSurface, rememberSurface, transferSurface } from '../visual/surfaceSnapshots'
import {clearTextMetrics} from '../visual/blerg/layout'
import { HEAVY_CHART } from '../visual/vegaBudget'
import type { VisualKind } from '../visual/policy'
import { observeVisualTheme, readVisualTheme, type VisualTheme } from '../visual/theme'
import { visualSnapshotId } from '../model/visualKind'
import { useViewerHost } from './ViewerHost'
import CodeBlock from './CodeBlock'
import './visual.css'
import DashboardBlock from './DashboardBlock'

function Surface({kind, source, theme, zoom = 1, fill = false, stretch = false, dashboardGuides = false, onError, onRendered}: {
  kind: VisualKind; source: string; theme: VisualTheme; zoom?: number
  /** BLG-467: true in a tab or full screen — the one thing a single chart/diagram has to itself,
   *  so it fills that space (even past an explicit spec width) rather than leaving most of it
   *  empty. False in a chat message, where an explicit width is kept (shrunk only if the message
   *  is narrower) and Mermaid's own CSS cap (visual.css) already handles its own growth. */
  fill?: boolean
  dashboardGuides?: boolean
  stretch?: boolean
  onError: (reason: string) => void
  /** The current drawing, for a consumer (a tab's download actions) that needs its SVG/size —
   *  The last complete drawing remains exportable while a replacement is prepared. */
  onRendered?: (visual: RenderedVisual | null, committedSource?: string) => void
}) {
  // BLG-468 §4: a flowchart node/subgraph naming a card key opens it through the same
  // RefContext/store lending contract chat text already uses (RefLinks.tsx's useChipOpener) —
  // absent outside a chat that lends refs, in which case a chip still draws but does nothing.
  const onChipOpen = useChipOpener()
  const chipOpen = useRef(onChipOpen)
  useLayoutEffect(() => { chipOpen.current = onChipOpen }, [onChipOpen])
  const target = useRef<HTMLDivElement>(null)
  const input = useRef({source, theme, onError})
  const redraw = useRef<(() => void) | null>(null)
  useLayoutEffect(() => {
    input.current = {source, theme, onError}
    redraw.current?.()
  }, [source, theme, onError])
  useLayoutEffect(() => {
    let stopped = false
    let generation = 0
    let lastFailure = -Infinity, suppressed = 0
    let refusedSource: string | undefined
    let visualSource: string | undefined
    let visualTheme: VisualTheme | undefined
    let visualModel: Record<string, unknown> | undefined
    let visual: RenderedVisual | undefined
    let prepared: RenderedVisual | undefined
    let pointer: {x: number; y: number} | undefined
    const node = target.current
    if (!node) return
    const unregisterPrint = registerPrintVisual(node, () => visual?.snapshot?.().svg ?? visual?.svg)
    // node.clientWidth/clientHeight include the surface's own padding; a filled child lays out
    // inside that padding, so the space actually available to it is smaller by that amount.
    const available = () => {
      const style = getComputedStyle(node)
      return {
        width: Math.max(0, node.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0)),
        height: Math.max(0, node.clientHeight - (parseFloat(style.paddingTop) || 0) - (parseFloat(style.paddingBottom) || 0)),
      }
    }
    const footerHeight = () => kind === 'vega-lite' && fill ? node.querySelector<HTMLElement>('.visual-blerg-table > summary')?.getBoundingClientRect().height ?? 0 : 0
    const nativeSize = (model: Record<string, unknown>, fit: VisualFit) => {
      const size = blergChartSize(model, fit)
      const footer = footerHeight()
      // Width is fitted against the surface, never against the footer-reduced
      // height. Native minimum plot height may require scrolling in tiny panes.
      return footer > 0 && fit.available.height > 0
        ? {width: size.width, height: Math.max(1, Math.min(size.height, fit.available.height - footer))}
        : size
    }
    let width = node.clientWidth
    // Only tracked (and only triggers a redraw) when filling: a message surface's own height
    // comes from what it just drew, and comparing it here would retrigger the very resize this
    // is meant to react to. A tab's or full screen's surface has its height set by its own flex
    // layout (visual.css), independent of its content, so this never loops there.
    let height = fill ? node.clientHeight : 0
    let frame = 0
    let visibleHost: HTMLElement | undefined
    let controller: AbortController | undefined
    let exactBox: {width: number; height: number} | undefined
    let hiddenInvalidated = false
    let offeredSource: string | undefined
    let releaseSnapshot: (() => void) | undefined
    let fontInvalidated = false
    const captureSnapshot = () => {if (visual && visibleHost && visualSource) rememberSurface(visualSource, visibleHost, visual.width, visual.height)}
    const offerSnapshot = () => {if (offeredSource === visualSource) return; releaseSnapshot?.(); offeredSource = visualSource; if (visualSource) releaseSnapshot = offerSurface(visualSource, captureSnapshot)}
    const warn = (event: string, error: unknown) => {
      const now = performance.now()
      if (now - lastFailure < 1000) { suppressed++; return }
      console.warn(event, {kind, cause: error instanceof Error ? error.name : 'UnknownError', suppressed})
      lastFailure = now; suppressed = 0
    }
    const notice = document.createElement('p')
    notice.className = 'artifact-status'
    notice.setAttribute('role', 'status')
    notice.textContent = 'Chart update unavailable. Showing the last complete drawing.'
    const publishExact = (box: {width: number; height: number}, committedGeneration: number, mounted = false) => {
      if (!visual || !visibleHost) return
      // Measurements describe the whole content box. Only the renderer's height
      // budget excludes its sibling footer; lifecycle observers need both distinct.
      box = available()
      if (mounted && fill && visual.resize && visualModel && footerHeight() > 0) {
        try {
          visual.resize(nativeSize(visualModel, {available: box, fill, stretch, dashboardGuides}), committedGeneration)
        } catch (error) {
          warn('Native chart footer fit failed', error)
          node.dataset.sceneState = 'retained'; node.append(notice)
          return
        }
      }
      visibleHost.style.width = `${visual.width}px`
      hiddenInvalidated = false
      exactBox = box
      node.dataset.sceneGeneration = String(committedGeneration)
      node.dataset.sceneWidth = String(visual.width)
      node.dataset.sceneHeight = String(visual.height)
      node.dataset.measuredWidth = String(box.width)
      node.dataset.measuredHeight = String(box.height)
      node.dataset.sceneState = 'exact'
      const heightOverflow = fill && visual.height + footerHeight() > box.height + 1
      node.dataset.sceneOverflow = heightOverflow ? 'scroll' : 'fit'
      if (heightOverflow) { node.tabIndex = 0; node.setAttribute('aria-label', 'Scrollable chart') }
      else { node.removeAttribute('tabindex'); node.removeAttribute('aria-label') }
      node.removeAttribute('aria-busy'); notice.remove()
      offerSnapshot()
      if(kind==='vega-lite')node.dataset.liveSupport=String(!!visual.applyData && !!visualModel && supportsNativeDataUpdates(visualModel))
      if(kind==='vega-lite')node.dispatchEvent(new CustomEvent('blerg-chart-live-support', {bubbles:true,detail:!!visual.applyData && !!visualModel && supportsNativeDataUpdates(visualModel)}))
      reportFrame(); onRendered?.(visual, visualSource)
    }
    const reportFrame = () => node.dispatchEvent(new Event('blerg-chart-frame', {bubbles: true}))
    const fitRetained = () => {
      if (!visibleHost) return
      const box = available()
      const sceneWidth = visual?.width ?? Number(visibleHost.dataset.snapshotWidth)
      const sceneHeight = visual?.height ?? Number(visibleHost.dataset.snapshotHeight)
      if (box.width <= 0) { visibleHost.style.width = `${sceneWidth}px`; return }
      visibleHost.style.width = `${Math.max(1, Math.min(sceneWidth, box.width, fill && box.height > 0 ? box.height * sceneWidth / sceneHeight : Infinity))}px`
      node.dataset.sceneState = 'retained'
      reportFrame()
    }
    if (kind === 'vega-lite') {
      const snapshot = transferSurface(input.current.source)
      if (snapshot) { visibleHost = snapshot; node.replaceChildren(snapshot); fitRetained() }
    }
    const loading = () => {
      const skeleton = document.createElement('div')
      skeleton.className = 'visual-loading'
      skeleton.setAttribute('aria-label', kind === 'vega-lite' ? 'Drawing chart' : 'Drawing diagram')
      node.setAttribute('aria-busy', 'true')
      node.replaceChildren(skeleton)
    }
    const rememberPointer = (event: PointerEvent) => { pointer = {x: event.clientX, y: event.clientY} }
    const forgetPointer = () => { pointer = undefined }
    node.addEventListener('pointermove', rememberPointer)
    node.addEventListener('pointerleave', forgetPointer)
    const draw = () => {
      const {source, theme} = input.current
      const box = available()
      if (kind === 'vega-lite' && (box.width <= 0 || (fill && box.height <= 0))) {hiddenInvalidated = true; return}
      if (kind === 'vega-lite' && visual && (source === visualSource || source === refusedSource) && theme === visualTheme && exactBox?.width === box.width && (!fill || exactBox.height === box.height)) {
        prepared?.dispose(); prepared = undefined
        controller?.abort(); cancelAnimationFrame(frame)
        if (node.dataset.sceneState !== 'exact') publishExact(box, Number(node.dataset.sceneGeneration))
        return
      }
      const currentGeneration = ++generation
      prepared?.dispose()
      prepared = undefined
      cancelAnimationFrame(frame)
      controller?.abort()
      controller = new AbortController()
      if (kind !== 'vega-lite') { visual?.dispose(); visual = undefined }
      if (!visual) onRendered?.(null)
      // Each rendering owns a separate host, including late work after a resize.
      const host = document.createElement('div')
      if (kind === 'vega-lite') { host.className = 'visual-scene'; if (!visual && !visibleHost) loading() }
      else node.replaceChildren(host)
      const fit: VisualFit = {available: available(), fill, stretch, dashboardGuides}
      if (kind === 'vega-lite' && visual?.applyData && source !== visualSource && source !== refusedSource) {
        let dataOnly = false
        try {
          const model=getChartModel(source)
          dataOnly = !!visualModel && sameDataStructure(visualModel,model)
          if(visual.applyData(model,currentGeneration)) {
            refusedSource=undefined;visualSource=source;visualModel=model
            if(theme !== visualTheme)visual.setTheme?.(theme)
            if(exactBox?.width !== fit.available.width || (fill && exactBox.height !== fit.available.height))visual.resize?.(nativeSize(model,fit),currentGeneration)
            visualTheme=theme
            publishExact(fit.available,currentGeneration)
            return
          }
        } catch(error) {warn('Native chart data update failed',error)}
        // A refused data changeset must not bypass its identity/schema checks by
        // recompiling the same rows as a fresh scene (or a Vega snapshot).
        if(dataOnly){refusedSource=source;warn('Native chart data update refused',new Error('RefusedChangeset'));node.append(notice)}
      }
      if (kind === 'vega-lite' && visual?.resize && (source === visualSource || source === refusedSource) && fit.available.width > 0 && (!fill || fit.available.height > 0)) {
        try {
          if (theme !== visualTheme) visual.setTheme?.(theme)
          visual.resize(nativeSize(visualModel ?? getChartModel(source), fit), currentGeneration)
          visualTheme = theme
          notice.remove()
          if (pointer) visibleHost?.querySelector('svg')?.dispatchEvent(new PointerEvent('pointermove', {clientX: pointer.x, clientY: pointer.y}))
          publishExact(fit.available, currentGeneration)
          if(source === refusedSource)node.append(notice)
          return
        } catch (error) {
          warn('Native chart resize failed', error)
        }
      }
      if(source === refusedSource){fitRetained();node.append(notice);return}
      if (visibleHost && kind === 'vega-lite') fitRetained()
      if (kind === 'vega-lite' && (fit.available.width <= 0 || (fill && fit.available.height <= 0))) return
      const active = controller
      const openChip = chipOpen.current ? (key: string) => chipOpen.current?.(key) : undefined
      const drawing = kind === 'vega-lite' ? Promise.resolve(document.fonts?.ready).then(() => renderVisual(kind, source, host, theme, active.signal, fit, openChip)) : renderVisual(kind, source, host, theme, active.signal, fit, openChip)
      void drawing.then(rendered => {
        if (stopped || active.signal.aborted) { rendered.dispose(); return }
        if (kind !== 'vega-lite') {
          if (host.parentElement !== node) rendered.dispose()
          else { visual = rendered; onRendered?.(rendered) }
          return
        }
        // Rendering happens off-DOM. Commit only at the measured size, at a frame
        // boundary; hidden tabs and late worker results never flash a stale drawing.
        prepared = rendered
        frame = requestAnimationFrame(() => {
          prepared = undefined
          if (stopped || active.signal.aborted) { rendered.dispose(); return }
          const current = available()
          if (current.width !== fit.available.width || (fill && current.height !== fit.available.height)) {
            rendered.dispose()
            draw()
            return
          }
          const scrollTop = node.scrollTop, scrollLeft = node.scrollLeft
          const previous = visual
          node.replaceChildren(host)
          visibleHost = host
          host.style.width = `${rendered.width}px`
          previous?.dispose()
          node.scrollTop = scrollTop
          node.scrollLeft = scrollLeft
          if (pointer) host.querySelector('canvas, svg')?.dispatchEvent(new PointerEvent('pointermove', {clientX: pointer.x, clientY: pointer.y}))
          node.removeAttribute('aria-busy')
          visual = rendered
          refusedSource = undefined
          visualSource = source
          visualModel = rendered.resize || rendered.applyData ? getChartModel(source) : undefined
          visualTheme = theme
          publishExact(fit.available, currentGeneration, true)
        })
      }, (error: unknown) => {
        if (!stopped && !active.signal.aborted) {
          warn('Visual drawing failed', error)
          if (visual || visibleHost) node.append(notice)
          else input.current.onError(failureReason(error))
        }
      })
    }
    redraw.current = draw
    draw()
    const fontsChanged = (event: Event) => {
      if (!visual || fontInvalidated) return
      const families = input.current.theme.fontFamily.split(',').map(f => f.trim().replace(/^['"]|['"]$/g, '').toLowerCase())
      const faces = (event as FontFaceSetLoadEvent).fontfaces ?? []
      if (!faces.some(face => families.includes(face.family.replace(/^['"]|['"]$/g, '').toLowerCase()))) return
      fontInvalidated = true
      clearTextMetrics(); exactBox = undefined; draw()
    }
    if (kind === 'vega-lite') document.fonts?.addEventListener('loadingdone', fontsChanged)
    // BLG-468 §4 (review round 2): a flowchart reflows live as the pane resizes, throttled to
    // once per frame (cancelAnimationFrame/requestAnimationFrame below), the same shared observer
    // vega-lite/fill already used. Two feedback loops surfaced while wiring this up for the
    // non-fill and fill cases, both fixed at their CSS root rather than worked around here:
    //  - a diagram's own CSS-scaled size toggling whether its shrink-wrapped chat card needed a
    //    scrollbar, changing the card's own clientWidth, re-triggering this observer — fixed by
    //    chat.css's own `.agent-card:has(.visual-block)` rule (the card no longer shrink-wraps
    //    around a diagram at all) plus drawing at an explicit width computed from that now-stable
    //    measurement (render.ts's own layoutForWidth/displaySize), not a CSS percentage a
    //    collapsed ancestor could resolve against itself;
    //  - `.visual-surface`'s own overflow:auto scrollbar toggling on/off exactly because of the
    //    content a fill-sizing calculation was driving (eating into the width that calculation
    //    already used, drawing it shorter, no longer needing the scrollbar, widening it back) —
    //    fixed by `scrollbar-gutter: stable` (visual.css), so that width never depends on whether
    //    the box happens to be scrolling right now.
    // Both were measured as genuine, continuous redraw loops (hundreds/second), not just "a
    // couple of settling redraws" after mount.
    const unobserve = kind !== 'vega-lite' && kind !== 'mermaid' && !fill ? () => {} : observeSurface(node, () => {
      if (node.clientWidth <= 0 || (fill && node.clientHeight <= 0)) {captureSnapshot(); return}
      if (!hiddenInvalidated && node.clientWidth === width && (!fill || node.clientHeight === height)) return
      width = node.clientWidth
      if (fill) height = node.clientHeight
      if (kind === 'vega-lite') controller?.abort()
      cancelAnimationFrame(frame)
      // Native resize writes scene dimensions. Defer it too: writing during observer
      // delivery can resize this surface again in the same layout pass.
      frame = requestAnimationFrame(draw)
    })
    return () => { unregisterPrint(); captureSnapshot(); releaseSnapshot?.(); redraw.current = null; document.fonts?.removeEventListener('loadingdone', fontsChanged); stopped = true; controller?.abort(); cancelAnimationFrame(frame); unobserve(); node.removeEventListener('pointermove', rememberPointer); node.removeEventListener('pointerleave', forgetPointer); node.replaceChildren(); visual?.dispose(); prepared?.dispose(); onRendered?.(null) }
  }, [kind, fill, stretch, dashboardGuides, onRendered])
  return <div className="visual-surface-wrap">
    <div className="visual-surface" ref={target} style={{zoom}} />
    <ChipOverlay root={target} />
  </div>
}

function FullScreen({kind, source, theme, onClose, onError}: {
  kind: VisualKind; source: string; theme: VisualTheme; onClose: () => void; onError: (reason: string) => void
}) {
  const dialog = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const [zoom, setZoom] = useState(1)
  const [rendered, setRendered] = useState<RenderedVisual | null>(null)
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
  // button-guard-ignore: useBackdropClose supplies pointer handlers only; this overlay is not a button
  return createPortal(<div className="artifact-overlay artifact-overlay-viewer" style={style} {...backdrop}>
    <div className="artifact-dialog artifact-viewer visual-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}
      tabIndex={-1} ref={dialog} onKeyDown={event => {
        if (event.key === 'Escape') {event.stopPropagation(); onClose()}
        if (event.key === 'Tab') {
          const buttons = dialog.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')
          const first = buttons?.[0]
          const last = buttons?.[buttons.length - 1]
          if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
            event.preventDefault(); last?.focus()
          } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
            event.preventDefault(); first?.focus()
          }
        }
      }}>
      <header className="artifact-header"><h2 id={titleId}>{kind === 'mermaid' ? 'Diagram' : 'Chart'}</h2>
        <button type="button" onClick={() => setZoom(value => Math.max(0.25, value - 0.25))} aria-label="Zoom out" title="Zoom out">−</button>
        <button type="button" onClick={() => setZoom(1)} aria-label="Reset zoom" title="Reset zoom">{Math.round(zoom * 100)}%</button>
        <button type="button" onClick={() => setZoom(value => Math.min(4, value + 0.25))} aria-label="Zoom in" title="Zoom in">+</button>
        <VisualExportActions kind={kind} source={source} visual={rendered} theme={theme} content={() => dialog.current?.querySelector('.visual-full-scroll') ?? null} />
        <button type="button" className="artifact-close" onClick={onClose} aria-label="Close" title="Close">✕</button>
      </header>
      <div className="visual-full-scroll"><Surface kind={kind} source={source} theme={theme} zoom={zoom} fill onError={onError} onRendered={setRendered} /></div>
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

export default function VisualBlock({kind, source, onRendered, hostedTab = false, stretch = false, dashboardGuides = false, fileContext}: {
  kind: VisualKind; source: string
  fileContext?: {sessionId: string; files: TransportFiles}
  /** The current drawing, for a host (a tab's download actions, §3.4) that needs its SVG/size. */
  onRendered?: (visual: RenderedVisual | null, committedSource?: string) => void
  /** True when this block is already a tab's own body (VisualViewer, or a published file's
   *  ArtifactViewer tab) — never offer Tab there: a snapshot tab popping out its own snapshot
   *  reopens itself, and a published file's tab popping out would freeze a copy that stops
   *  following versions. Published artifact previews also use this mode: View opens the
   *  backing file, and the drawing fits the reserved preview area. */
  hostedTab?: boolean
  /** A dashboard chart owns a rectangular plot box instead of an authored aspect ratio. */
  dashboardGuides?: boolean
  stretch?: boolean
}) {
  // Charts keep a surface across source revisions so prepared replacements can swap atomically.
  if (kind === 'dashboard') return <DashboardBlock source={source} fileContext={fileContext} />
  return <VisualContent key={kind === 'vega-lite' ? kind : `${kind}:${source}`} kind={kind} source={source} onRendered={onRendered} hostedTab={hostedTab} stretch={stretch} dashboardGuides={dashboardGuides} />
}

function VisualContent({kind, source, onRendered, hostedTab, stretch, dashboardGuides}: {kind: VisualKind; source: string; onRendered?: (visual: RenderedVisual | null, committedSource?: string) => void; hostedTab: boolean; stretch: boolean; dashboardGuides:boolean}) {
  const root = useRef<HTMLElement>(null)
  const [theme, setTheme] = useState<VisualTheme>()
  useEffect(() => {
    const node = root.current
    if (!node) return
    setTheme(readVisualTheme(node))
    return observeVisualTheme(node, setTheme)
  }, [])
  const [failed, setFailed] = useState<{source: string; reason: string} | null>(null)
  const [showSource, setShowSource] = useState(false)
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])
  // React state setters are stable, preventing renders from restarting chart initialization.
  const fail = useCallback((reason: string) => {
    console.warn('visual_draw_failed', { kind, cause: 'renderer_rejected' })
    setOpen(false); setFailed({source, reason})
  }, [kind, source])
  const host = useViewerHost()
  // BLG-471: a renderer's own toolbar (gantt's zoom/fit-to-width, RenderedVisual.toolbar) belongs
  // in the app's existing chrome, never floating over the drawing — the tab's own ViewerToolbar
  // when this block is hosted there (ViewerActionsContext, provided by VisualViewer.tsx — the
  // same mechanism DiffView.tsx/ImageView.tsx already use for their own zoom controls), or this
  // block's own `.visual-actions` row otherwise. Reset to null whenever Surface's own `onRendered`
  // reports null (a redraw in progress, a failure, or `showSource` unmounting it) so a stale
  // toolbar from the previous drawing can never linger.
  const [rendered, setRendered] = useState<RenderedVisual | null>(null)
  const [toolbar, setToolbar] = useState<HTMLElement | null>(null)
  const toolbarHost = useContext(ViewerActionsContext)
  const localToolbarSlot = useRef<HTMLSpanElement>(null)
  const handleRendered = useCallback((visual: RenderedVisual | null, committedSource?: string) => { setRendered(visual); setToolbar(visual?.toolbar ?? null); if(committedSource===undefined)onRendered?.(visual);else onRendered?.(visual, committedSource) }, [onRendered])
  useEffect(() => {
    if (!toolbar) return
    const target = toolbarHost ?? localToolbarSlot.current
    if (!target) return
    target.appendChild(toolbar)
    return () => { toolbar.remove() }
  }, [toolbar, toolbarHost])
  const openTab = () => host?.open({ kind: 'visual', id: visualSnapshotId(kind, source), visualKind: kind, source, label: kind === 'mermaid' ? 'Diagram' : 'Chart' })
  // A refusal explains itself; source stays behind the same explicit toggle as a drawing.
  if (failed !== null && failed.source === source) return <div className="visual-failed" data-testid="visual-failed">
    <div className="visual-actions">
      <ViewerAction icon="raw" label={showSource ? 'Show the drawing' : 'Show source'} pressed={showSource} onClick={() => setShowSource(value => !value)} />
      {!hostedTab && host && <ViewerAction icon="popOut" label="Open in a tab" onClick={openTab} />}
    </div>
    <p className="visual-failed-reason" role="status">{failed.reason === HEAVY_CHART ? failed.reason : `Couldn’t draw this ${kind === 'mermaid' ? 'diagram' : 'chart'}: ${failed.reason}`}</p>
    {showSource && <CodeBlock closed>{source}</CodeBlock>}
  </div>
  return <section ref={root} className="visual-block" aria-label={kind === 'mermaid' ? 'Diagram' : 'Chart'}>
    <div className="visual-actions">
      {!hostedTab && theme && <VisualExportActions kind={kind} source={source} visual={rendered} theme={theme} content={() => root.current} />}
      {!toolbarHost && <span ref={localToolbarSlot} className="visual-actions-renderer-slot" />}
      <ViewerAction icon="raw" label={showSource ? 'Show the drawing' : 'Show source'} pressed={showSource} onClick={() => setShowSource(value => !value)} />
      {typeof navigator !== 'undefined' && navigator.clipboard && <ViewerAction icon="copy" label={copied ? 'Copied' : 'Copy source'} onClick={() => {
        void navigator.clipboard.writeText(source).then(() => setCopied(true), () => {})
      }} />}
      <ViewerAction icon="maximize" label="Full screen" onClick={() => setOpen(true)} />
      {!hostedTab && host && <ViewerAction icon="popOut" label="Open in a tab" onClick={openTab} />}
    </div>
    {showSource ? <CodeBlock closed>{source}</CodeBlock> : theme && <Surface kind={kind} source={source} theme={theme} fill={hostedTab} stretch={stretch} dashboardGuides={dashboardGuides} onError={fail} onRendered={handleRendered} />}
    {open && theme && <FullScreen kind={kind} source={source} theme={theme} onClose={() => setOpen(false)} onError={fail} />}
  </section>
}

/** A diagram or chart drawn small, with nothing around it (a hovercard's preview): the same
 *  renderer and sandbox as the block (a Mermaid diagram in its sandboxed frame, CSP default-src
 *  'none'); a failure says so in one line. */
export function VisualThumb({kind, source}: {kind: VisualKind; source: string}) {
  if (kind === 'dashboard') return <DashboardBlock source={source} />
  return <VisualDrawingThumb kind={kind} source={source} />
}

function VisualDrawingThumb({kind, source}: {kind: VisualKind; source: string}) {
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
