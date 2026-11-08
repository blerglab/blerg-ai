// VisualViewer: an inline chart or diagram popped out of the chat (BLG-451), drawn exactly as the
// block itself draws it (VisualBlock, the same renderer and sanitizer) -- with Download SVG/PNG
// and Download source in its own toolbar, since this tab has no backing file to fetch a download
// from. The source is a snapshot taken when the tab opened; it does not follow the live message.
import { useCallback, useId, useRef, useState } from 'react'
import VisualBlock from './VisualBlock'
import ViewerToolbar, { ViewerAction, ViewerActionsContext } from './ViewerToolbar'
import { DASHBOARD_EXPORT_BLOCKED, payloadExportAllowed, exportPng, exportSvg, exportedAtLowerResolution, sourceExtension } from '../visual/visualExport'
import { readVisualTheme } from '../visual/theme'
import { saveBlob } from '../transport/files'
import type { RenderedVisual } from '../visual/render'
import type { VisualKind } from '../model/visualKind'

function fileSafe(label: string, fallback: string): string {
  const name = label.replace(/[\\/:*?"<>|]+/g, '_').trim()
  return name || fallback
}

export default function VisualViewer({ visualKind, source, label, onClose, embedded = false, kept = true }: {
  visualKind: VisualKind
  source: string
  label: string
  onClose: () => void
  embedded?: boolean
  /** False: the host's bounded snapshot store had no room for this one. It stays for this
   *  session; a reload will not bring it back. */
  kept?: boolean
}) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  // BLG-471: lets a hosted renderer's own toolbar (RenderedVisual.toolbar, VisualBlock.tsx) mount
  // into this tab's own ViewerToolbar instead of floating over the drawing — the same
  // ViewerActionsContext mechanism DiffView.tsx/ImageView.tsx already use.
  const [toolbarHost, setToolbarHost] = useState<HTMLSpanElement | null>(null)
  const [rendered, setRendered] = useState<RenderedVisual | null>(null)
  // Dashboard payload must never survive in a cache another visual kind can export.
  const onRendered = useCallback((visual: RenderedVisual | null) => setRendered(payloadExportAllowed(visualKind) ? visual : null), [visualKind])
  const [exportError, setExportError] = useState<string | null>(null)
  const [exportNotice, setExportNotice] = useState<string | null>(null)
  const exportName = fileSafe(label, visualKind === 'mermaid' ? 'diagram' : 'chart')

  function downloadSvg() {
    if (!rendered || !payloadExportAllowed(visualKind)) return
    setExportError(null)
    try { saveBlob(exportSvg(rendered), `${exportName}.svg`) }
    catch { console.warn('visual_export_failed', { cause: 'svg_export_rejected' }); setExportError('Could not export the SVG.') }
  }
  async function downloadPng() {
    if (!rendered || !payloadExportAllowed(visualKind)) return
    setExportError(null)
    setExportNotice(null)
    try {
      const theme = readVisualTheme(dialogRef.current ?? document.body)
      const blob = await exportPng(rendered, theme)
      saveBlob(blob, `${exportName}.png`)
      if (exportedAtLowerResolution(rendered)) setExportNotice('Exported at a lower resolution to stay within size limits.')
    } catch { console.warn('visual_export_failed', { cause: 'png_export_rejected' }); setExportError('Could not export the PNG.') }
  }
  function downloadSource() {
    saveBlob(new Blob([source], { type: 'text/plain' }), `${exportName}${sourceExtension(visualKind)}`)
  }

  return (
    <div className={embedded ? 'embedded-viewer' : 'artifact-overlay artifact-overlay-viewer'} data-testid="visual-viewer">
      <div
        ref={dialogRef}
        className="artifact-dialog artifact-viewer"
        role={embedded ? 'region' : 'dialog'}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => {
          if (!embedded && e.key === 'Escape') { e.stopPropagation(); onClose() }
        }}
      >
        {/* The tab already names this chart/diagram; the toolbar repeats the title only for a
            modal standing alone (showPath, as ArtifactViewer's embedded file tabs already do). */}
        <ViewerToolbar exports={<>
          <ViewerAction icon="downloadSvg" label="Download SVG" disabled={!rendered || !payloadExportAllowed(visualKind)} onClick={downloadSvg} />
          <ViewerAction icon="downloadPng" label="Download PNG" disabled={!rendered || !payloadExportAllowed(visualKind)} onClick={() => void downloadPng()} />
          <ViewerAction icon="download" label="Download source" onClick={downloadSource} />
        </>} printDisabled={!rendered || !payloadExportAllowed(visualKind)} titleId={titleId} title={label} meta={visualKind === 'mermaid' ? 'Mermaid snapshot' : 'Vega-Lite snapshot'} copyLabel="Copy title" showPath={!embedded}>

          <span className="viewer-control-slot" ref={setToolbarHost} />
          {!embedded && <ViewerAction icon="close" label="Close" onClick={onClose} />}
        </ViewerToolbar>
        {exportError && <div className="artifact-error" role="alert">{exportError}</div>}
        {visualKind === 'dashboard' && <p className="artifact-status" role="status">{DASHBOARD_EXPORT_BLOCKED}</p>}
        {exportNotice && <p className="artifact-status" role="status">{exportNotice}</p>}
        {!kept && (
          <p className="artifact-status" data-testid="visual-not-kept">This {visualKind === 'mermaid' ? 'diagram' : 'chart'} was too large to keep — it won’t be here after a reload.</p>
        )}
        <div className="artifact-body">
          <ViewerActionsContext.Provider value={toolbarHost}>
            <VisualBlock kind={visualKind} source={source} onRendered={onRendered} hostedTab />
          </ViewerActionsContext.Provider>
        </div>
      </div>
    </div>
  )
}
