// The small picture a PDF's inline preview shows: its first page drawn once on a canvas, kept as
// a compressed data URL. The pdf.js document is destroyed as soon as the page is drawn, so
// neither the file's bytes nor a parsed document outlive the call.
import { loadPdfJs } from './loader'

/** The width the first page is drawn at (a thumbnail, sharp enough for a phone's card). */
export const PDF_THUMB_WIDTH = 480

export async function renderPdfThumb(blob: Blob): Promise<string> {
  const [pdfjs, data] = await Promise.all([loadPdfJs(), blob.arrayBuffer()])
  const task = pdfjs.getDocument({ data })
  try {
    const doc = await task.promise
    const page = await doc.getPage(1)
    const base = page.getViewport({ scale: 1 })
    const viewport = page.getViewport({ scale: PDF_THUMB_WIDTH / base.width })
    const canvas = document.createElement('canvas')
    canvas.width = Math.floor(viewport.width)
    canvas.height = Math.floor(viewport.height)
    await page.render({ canvas, viewport }).promise
    const url = canvas.toDataURL('image/jpeg', 0.82)
    canvas.width = 0
    canvas.height = 0
    return url
  } finally {
    void task.destroy()
  }
}
