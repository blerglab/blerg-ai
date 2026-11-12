import DOMPurify from 'dompurify'
import { readVisualTheme } from '../visual/theme'
import { saveBlob } from '../transport/files'
import { sanitizeSvg } from '../visual/policy'

/** Read rendered cell text, including explicit line breaks, rather than Markdown syntax. */
export function tableRows(table: HTMLTableElement): string[][] {
  return Array.from(table.rows, row => Array.from(row.cells, cell => {
    const copy = cell.cloneNode(true) as HTMLElement
    copy.querySelectorAll('br').forEach(br => br.replaceWith('\n'))
    return copy.textContent ?? ''
  }))
}
export function tableCsv(table: HTMLTableElement): string {
  return tableRows(table).map(row => row.map(value => {
    const safe = /^[\s]*[=+\-@]/.test(value) || /^[\t\r]/.test(value) ? `'${value}` : value
    return `"${safe.replaceAll('"', '""')}"`
  }).join(',')).join('\r\n') + '\r\n'
}
export function tableMarkdown(table: HTMLTableElement): string {
  const rows = tableRows(table)
  const line = (row: string[]) => `| ${row.map(cell => cell.replaceAll('\\', '\\\\').replaceAll('|', '\\|').replaceAll('\n', '<br>')).join(' | ')} |`
  return rows.length ? [line(rows[0]), line(rows[0].map(() => '---')), ...rows.slice(1).map(line)].join('\n') + '\n' : ''
}
const extensions: Record<string, string> = { javascript: 'js', typescript: 'ts', python: 'py', bash: 'sh', yaml: 'yaml', rust: 'rs', markdown: 'md', xml: 'xml', dockerfile: 'Dockerfile', makefile: 'Makefile', gomod: 'mod', gosum: 'sum' }
export function codeFilename(lang: string | null): string {
  return lang === 'dockerfile' ? 'Dockerfile' : lang === 'makefile' ? 'Makefile' : `code.${lang ? extensions[lang] ?? lang : 'txt'}`
}
export function downloadText(text: string, name: string, type = 'text/plain') {
  saveBlob(new Blob([text], { type: `${type};charset=utf-8` }), name)
}

const visualSnapshots = new WeakMap<HTMLElement, () => string | undefined>()
export function registerPrintVisual(node: HTMLElement, snapshot: () => string | undefined): () => void {
  visualSnapshots.set(node, snapshot)
  return () => { visualSnapshots.delete(node) }
}

// A separate, local document gives print deterministic pagination without app chrome or theme.
export const CONTENT_PRINT_CSS = `
@page { margin: 12mm; }
:root { color-scheme: light; }
body { color: black; background: white; font: 11pt system-ui, sans-serif; margin: 0; }
* { box-sizing: border-box; max-width: 100% !important; overflow: visible !important; max-height: none !important; }
table { width: 100%; table-layout: fixed; border-collapse: collapse; }
thead { display: table-header-group; } tr { break-inside: avoid; }
th, td { border: 1px solid gray; padding: 5pt; overflow-wrap: anywhere; }
pre, code { white-space: pre-wrap; overflow-wrap: anywhere; } pre { font-size: 9pt; }
img, svg { max-width: 100%; height: auto; } svg { width: 100%; }
button, [role=toolbar], .visual-actions, .md-export-actions, .md-codeblock-actions, .chat-sr-only { display: none !important; }
a { color: inherit; text-decoration: none; }
`
export function printDocument(content: HTMLElement): string {
  const clone = content.cloneNode(true) as HTMLElement
  // Sandboxed chart frames cannot be cloned; use the renderer's sanitized export snapshot.
  const surfaces = content.querySelectorAll<HTMLElement>('.visual-surface')
  clone.querySelectorAll<HTMLElement>('.visual-surface').forEach((node, i) => {
    const svg = visualSnapshots.get(surfaces[i])?.()
    if (svg) {
      const theme = readVisualTheme(surfaces[i])
      const colors = new Map([[theme.surface, 'white'], [theme.surface2, 'white'], [theme.fg, 'black'], [theme.muted, 'dimgray'], [theme.border, 'gray']])
      node.innerHTML = sanitizeSvg(svg.replace(/#[\da-f]{6}/gi, color => colors.get(color.toLowerCase()) ?? color))
    }
  })
  // Static HTML documents remain printable without executing their scripts in the print frame.
  const staticHtml = clone.querySelector('iframe[srcdoc]') !== null
  clone.querySelectorAll<HTMLIFrameElement>('iframe[srcdoc]').forEach(frame => {
    const page = document.createElement('div')
    page.innerHTML = DOMPurify.sanitize(frame.srcdoc, { FORBID_TAGS: ['script', 'style', 'iframe', 'button', 'input', 'form'] })
    frame.replaceWith(page)
  })
  const originals = content.querySelectorAll('canvas')
  clone.querySelectorAll('canvas').forEach((canvas, i) => {
    const image = document.createElement('img')
    image.src = originals[i].toDataURL()
    canvas.replaceWith(image)
  })
  clone.querySelectorAll('button, [role=toolbar], script, style, iframe, .visual-actions, .md-export-actions, .md-codeblock-actions, .chat-sr-only, .code-viewer-gutter, .workspace-line-band').forEach(node => { if (node.localName !== 'style' || !node.closest('svg')) node.remove() })
  clone.querySelectorAll('*').forEach(node => {
    const svg = !!node.closest('svg') // Sanitized SVG IDs, classes and styles define its geometry.
    for (const attribute of [...node.attributes]) {
      if (attribute.name.startsWith('on') || !svg && ['style', 'class', 'id', 'data-print-svg'].includes(attribute.name)) node.removeAttribute(attribute.name)
    }
  })
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src blob: data:; style-src 'unsafe-inline'"><title>Print</title><style>${CONTENT_PRINT_CSS}</style></head><body>${staticHtml ? '<p>Print includes static HTML content. Scripts and live changes are not included.</p>' : ''}${clone.innerHTML}</body></html>`
}
export async function printContent(content: HTMLElement | null): Promise<void> {
  if (!content) throw new Error('Print content is unavailable')
  const pdf = content.querySelector<HTMLIFrameElement>('iframe.artifact-frame[src^="blob:"]:not([srcdoc])')
  if (pdf) {
    if (!pdf.contentWindow) throw new Error('PDF print window is unavailable')
    pdf.contentWindow.focus()
    pdf.contentWindow.print()
    return
  }
  const html = printDocument(content)
  document.querySelector('iframe[data-content-print]')?.remove()
  const frame = document.createElement('iframe')
  frame.dataset.contentPrint = ''
  frame.title = 'Print preview'
  frame.style.cssText = 'position:fixed;left:-10000px;width:800px;height:1000px;border:0'
  const loaded = new Promise<void>((resolve, reject) => { frame.onload = () => resolve(); frame.onerror = () => reject(new Error('Print document could not load')) })
  frame.srcdoc = html
  document.body.append(frame)
  try {
    await loaded
    const target = frame.contentWindow
    if (!target) throw new Error('Print window is unavailable')
    await Promise.all(Array.from(frame.contentDocument?.images ?? [], image => image.decode()))
    await frame.contentDocument?.fonts.ready
    target.addEventListener('afterprint', () => frame.remove(), { once: true })
    target.focus()
    target.print()
  } catch (error) {
    frame.remove()
    throw error
  }
}
