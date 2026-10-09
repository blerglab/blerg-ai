// pdf.js, loaded on demand: the review mode of a PDF is the one place that needs it, and the chat
// bundle must not carry it for everyone else. The dynamic import makes Vite split pdf.js and its
// worker (./pdfjs.ts) into a chunk of their own.
export type PdfJs = typeof import('pdfjs-dist')

let loading: Promise<PdfJs> | null = null

/** The pdf.js module, loaded once and with its worker configured. */
export function loadPdfJs(): Promise<PdfJs> {
  loading ??= import('./pdfjs').then(m => m.default)
  return loading
}
