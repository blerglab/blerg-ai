import { useState } from 'react'
import { downloadText } from '../model/contentExport'
import { saveBlob } from '../transport/files'
import type { RenderedVisual } from '../visual/render'
import type { VisualKind } from '../visual/policy'
import type { VisualTheme } from '../visual/theme'
import { exportPng, exportSvg, exportedAtLowerResolution, payloadExportAllowed, sourceExtension } from '../visual/visualExport'
import { PrintAction, ViewerAction } from './ViewerToolbar'

export function VisualExportActions({ kind, source, visual, theme, content }: {
  kind: VisualKind; source: string; visual: RenderedVisual | null; theme: VisualTheme; content: () => HTMLElement | null
}) {
  const [status, setStatus] = useState('')
  const allowed = payloadExportAllowed(kind)
  function run(action: () => void | Promise<void>) {
    setStatus('')
    void Promise.resolve().then(action).catch(() => {
      console.warn('visual_export_failed', { cause: 'browser_export_rejected' })
      setStatus('Could not export this drawing. Try again.')
    })
  }
  return <>
    <PrintAction content={content} disabled={!visual || !allowed} />
    <ViewerAction icon="downloadSvg" label="Download SVG" disabled={!visual || !allowed} onClick={() => run(() => saveBlob(exportSvg(visual!), 'drawing.svg'))} />
    <ViewerAction icon="downloadPng" label="Download PNG" disabled={!visual || !allowed} onClick={() => run(async () => {
      saveBlob(await exportPng(visual!, theme), 'drawing.png')
      if (exportedAtLowerResolution(visual!)) setStatus('Exported at a lower resolution to stay within size limits.')
    })} />
    <ViewerAction icon="download" label="Download source" onClick={() => run(() => downloadText(source, `drawing${sourceExtension(kind)}`))} />
    {status && <span role="status" className="artifact-status">{status}</span>}
  </>
}
