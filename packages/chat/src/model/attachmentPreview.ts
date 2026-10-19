// Local-only attachment previews. Source bytes never become DOM resources: validated raster
// inputs and a PDF's first page are reduced to inert thumbnails; text stays plain text.
import {loadPdfJs} from '../pdf/loader'

export type AttachmentPreview = {kind: 'image' | 'pdf'; src: string} | {kind: 'text'; text: string}
export const MAX_PREVIEW_BYTES = 8 * 1024 * 1024
export const TEXT_PREVIEW_BYTES = 8192
const MAX_THUMB_URL = 128 * 1024
const MAX_RASTER_PIXELS = 24 * 1024 * 1024
const MAX_SIDE = 16000
const WIDTH = 160, HEIGHT = 120
let running = 0
const queue: Array<() => void> = []

function drain() { while (running < 2 && queue.length) queue.shift()!() }

/** At most two files decode at once. Aborted queued jobs immediately release their File. */
export function createAttachmentPreview(file: File, signal: AbortSignal): Promise<AttachmentPreview | undefined> {
  if (signal.aborted) return Promise.resolve(undefined)
  return new Promise(resolve => {
    const abort = () => {
      const at = queue.indexOf(run)
      if (at >= 0) { queue.splice(at, 1); resolve(undefined) }
    }
    const run = () => {
      signal.removeEventListener('abort', abort)
      if (signal.aborted) { resolve(undefined); return }
      running++
      const ctl = new AbortController()
      const cancel = () => {ctl.abort(); resolve(undefined)}
      const timeout = setTimeout(cancel, 8000)
      signal.addEventListener('abort', cancel, {once: true})
      void prepare(file, ctl.signal).then(result => resolve(ctl.signal.aborted ? undefined : result), () => resolve(undefined)).finally(() => {
        clearTimeout(timeout); signal.removeEventListener('abort', cancel); running--; drain()
      })
    }
    signal.addEventListener('abort', abort, {once: true})
    queue.push(run); drain()
  })
}

async function read(blob: Blob, signal: AbortSignal): Promise<ArrayBuffer> {
  if (signal.aborted) throw new Error('cancelled')
  if (blob.arrayBuffer) {
    const bytes = await blob.arrayBuffer()
    if (signal.aborted) throw new Error('cancelled')
    return bytes
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    const abort = () => reader.abort()
    signal.addEventListener('abort', abort, {once: true})
    reader.onloadend = () => {
      signal.removeEventListener('abort', abort)
      if (signal.aborted || reader.error || !(reader.result instanceof ArrayBuffer)) reject(new Error('unreadable'))
      else resolve(reader.result)
    }
    reader.readAsArrayBuffer(blob)
  })
}

async function prepare(file: File, signal: AbortSignal): Promise<AttachmentPreview | undefined> {
  if (!Number.isFinite(file.size) || file.size < 0) return
  const ext = file.name.includes('.') ? file.name.split('.').pop()?.toLowerCase() ?? '' : ''
  const mime = file.type.toLowerCase()
  if (['svg', 'svgz', 'html', 'htm'].includes(ext) || mime === 'image/svg+xml') return
  const visual = /^(png|jpg|jpeg|jpe|gif|webp|bmp|pdf)$/.test(ext) || mime.startsWith('image/') || mime === 'application/pdf'
  const text = !visual && (mime.startsWith('text/') || ['application/json', 'application/yaml', 'application/x-yaml'].includes(mime) || (!mime && /^(md|markdown|txt|log|json|csv|yaml|yml|toml|xml|go|py|js|ts|tsx|jsx|rs|sh|css|sql)$/.test(ext)))
  if (text && !mime.startsWith('image/')) {
    const bytes = new Uint8Array(await read(file.slice(0, TEXT_PREVIEW_BYTES), signal))
    if (bytes.includes(0)) return
    const decoded = new TextDecoder('utf-8', {fatal: true}).decode(bytes, {stream: bytes.length === TEXT_PREVIEW_BYTES})
    if ([...decoded].some(character => {const n = character.charCodeAt(0); return n < 32 && n !== 9 && n !== 10 && n !== 13})) return
    const excerpt = decoded.replace(/\r\n?/g, '\n').trim().slice(0, 277)
    return excerpt ? {kind: 'text', text: excerpt + (file.size > bytes.length || decoded.trim().length > 277 ? '…' : '')} : undefined
  }
  if (!file.size || file.size > MAX_PREVIEW_BYTES) return
  const bytes = new Uint8Array(await read(file, signal))
  if (ext === 'pdf' || mime === 'application/pdf') {
    if ((mime && mime !== 'application/pdf') || ext !== 'pdf' || !starts(bytes, [37, 80, 68, 70, 45])) return
    return pdfThumb(bytes, signal)
  }
  const raster = rasterInfo(bytes)
  if (!raster || (mime && mime !== raster.mime) || (ext && !raster.extensions.includes(ext))) return
  if (!validDimensions(raster.width, raster.height) || raster.width > MAX_SIDE || raster.height > MAX_SIDE || raster.width * raster.height > MAX_RASTER_PIXELS) return
  if (signal.aborted || typeof createImageBitmap !== 'function') return
  const size = fit(raster.width, raster.height)
  const bitmap = await createImageBitmap(new Blob([bytes], {type: raster.mime}), {resizeWidth: size.width, resizeHeight: size.height, resizeQuality: 'low'})
  try {
    if (signal.aborted) return
    const canvas = document.createElement('canvas')
    canvas.width = size.width; canvas.height = size.height
    try {
      const ctx = canvas.getContext('2d')
      if (!ctx) return
      ctx.drawImage(bitmap, 0, 0, size.width, size.height)
      return thumbnail('image', canvas)
    } finally {canvas.width = 0; canvas.height = 0}
  } finally {bitmap.close()}
}

function thumbnail(kind: 'image' | 'pdf', canvas: HTMLCanvasElement): AttachmentPreview | undefined {
  const src = canvas.toDataURL('image/png')
  return src.startsWith('data:image/png;base64,') && src.length <= MAX_THUMB_URL ? {kind, src} : undefined
}
function fit(width: number, height: number) {
  const scale = Math.min(1, WIDTH / width, HEIGHT / height)
  return {width: Math.max(1, Math.floor(width * scale)), height: Math.max(1, Math.floor(height * scale))}
}
function validDimensions(width: number, height: number) {return Number.isFinite(width) && Number.isFinite(height) && width > 0 && height > 0}

class NoPreviewResources {
  async fetch(): Promise<never> {throw new Error('Attachment previews do not load external resources')}
}
async function pdfThumb(bytes: Uint8Array, signal: AbortSignal): Promise<AttachmentPreview | undefined> {
  const pdfjs = await loadPdfJs()
  if (signal.aborted) return
  const options = {data: bytes, isEvalSupported: false, disableFontFace: true, useSystemFonts: false, useWorkerFetch: false,
    disableAutoFetch: true, disableRange: true, disableStream: true, useWasm: false, enableXfa: false,
    maxImageSize: 4 * 1024 * 1024, canvasMaxAreaInBytes: 4 * 1024 * 1024, isOffscreenCanvasSupported: false,
    isImageDecoderSupported: false, BinaryDataFactory: NoPreviewResources}
  const task = pdfjs.getDocument(options)
  let destroyed = false
  const destroy = () => {if (!destroyed) {destroyed = true; void task.destroy().catch(() => {})}}
  let rejectCancelled!: (error: Error) => void
  const cancelled = new Promise<never>((_resolve, reject) => {rejectCancelled = reject})
  const abort = () => {destroy(); rejectCancelled(new Error('cancelled'))}
  signal.addEventListener('abort', abort, {once: true})
  const canvas = document.createElement('canvas')
  try {
    const doc = await Promise.race([task.promise, cancelled])
    if (signal.aborted) return
    const page = await Promise.race([doc.getPage(1), cancelled])
    const base = page.getViewport({scale: 1})
    if (!validDimensions(base.width, base.height)) return
    const size = fit(base.width, base.height)
    const viewport = page.getViewport({scale: Math.min(size.width / base.width, size.height / base.height)})
    if (!validDimensions(viewport.width, viewport.height) || viewport.width > WIDTH || viewport.height > HEIGHT) return
    canvas.width = Math.max(1, Math.floor(viewport.width)); canvas.height = Math.max(1, Math.floor(viewport.height))
    await Promise.race([page.render({canvas, viewport, annotationMode: 0, background: '#ffffff'}).promise, cancelled])
    return signal.aborted ? undefined : thumbnail('pdf', canvas)
  } finally {
    signal.removeEventListener('abort', abort)
    destroy()
    canvas.width = 0; canvas.height = 0
  }
}

function starts(bytes: Uint8Array, signature: number[], at = 0) {return signature.every((value, i) => bytes[at + i] === value)}
function ascii(bytes: Uint8Array, at: number, value: string) {return starts(bytes, [...value].map(c => c.charCodeAt(0)), at)}
function rasterInfo(b: Uint8Array): {mime: string; extensions: string[]; width: number; height: number} | undefined {
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength)
  if (b.length >= 24 && starts(b, [137,80,78,71,13,10,26,10]) && ascii(b, 12, 'IHDR')) return {mime: 'image/png', extensions: ['png'], width: v.getUint32(16), height: v.getUint32(20)}
  if (b.length >= 10 && (ascii(b, 0, 'GIF87a') || ascii(b, 0, 'GIF89a'))) return {mime: 'image/gif', extensions: ['gif'], width: v.getUint16(6, true), height: v.getUint16(8, true)}
  if (b.length >= 26 && ascii(b, 0, 'BM') && v.getUint32(14, true) >= 40) return {mime: 'image/bmp', extensions: ['bmp'], width: v.getInt32(18, true), height: Math.abs(v.getInt32(22, true))}
  if (b.length >= 30 && ascii(b, 0, 'RIFF') && ascii(b, 8, 'WEBP')) {
    let width = 0, height = 0
    if (ascii(b, 12, 'VP8X')) {width = 1 + b[24] + (b[25] << 8) + (b[26] << 16); height = 1 + b[27] + (b[28] << 8) + (b[29] << 16)}
    else if (ascii(b, 12, 'VP8 ') && starts(b, [157,1,42], 23)) {width = v.getUint16(26, true) & 0x3fff; height = v.getUint16(28, true) & 0x3fff}
    else if (ascii(b, 12, 'VP8L') && b[20] === 47) {width = 1 + b[21] + ((b[22] & 63) << 8); height = 1 + (b[22] >> 6) + (b[23] << 2) + ((b[24] & 15) << 10)}
    return {mime: 'image/webp', extensions: ['webp'], width, height}
  }
  if (b.length >= 4 && starts(b, [255,216])) {
    for (let at = 2; at + 3 < b.length;) {
      if (b[at++] !== 255) return
      while (b[at] === 255) at++
      const marker = b[at++]
      if (marker === 217 || marker === 218) return
      const size = v.getUint16(at)
      if (size < 2 || at + size > b.length) return
      if ([192,193,194,195,197,198,199,201,202,203,205,206,207].includes(marker) && size >= 7) return {mime: 'image/jpeg', extensions: ['jpg', 'jpeg', 'jpe'], width: v.getUint16(at + 5), height: v.getUint16(at + 3)}
      at += size
    }
  }
}
