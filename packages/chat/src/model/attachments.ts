// Files a person attaches to a chat message. Each is uploaded at once by the transport; the
// message that is sent carries a fixed one-line note naming them, which is what makes the agent
// fetch them (`blerg-runner fetch --all`). The same note is read back here to draw the files as
// chips instead of raw text.
import { formatSize } from './artifacts'

/** The per-file limit assumed when the server does not say its own: what a runner older than
 *  0.2.1 took. A newer one advertises its limit (`limits.max_file_bytes`, 100 MiB by default),
 *  which a host passes to ChatView as `maxFileBytes`. */
export const MAX_ATTACHMENT_BYTES = 25 * 1024 * 1024
export const MAX_ATTACHMENTS_PER_MESSAGE = 10

/** A size limit the way the runner states it: whole MiB as "100 MiB", anything else as a size. */
export function formatLimit(bytes: number): string {
  const mib = 1024 * 1024
  return bytes >= mib && bytes % mib === 0 ? `${bytes / mib} MiB` : formatSize(bytes)
}

const NOTE_PREFIX = 'Attached files (fetch them with: blerg-runner fetch --all): '
const GENERIC_LINE = 'Please take a look at the attached files.'

export interface UploadedFile { id: string; name: string; size: number }

/** The name as it reads in the note: one line, whatever the file was called. */
const oneLine = (s: string) => s.replace(/[\r\n\t]+/g, ' ').trim()

export interface MessageAttachment { id?: string; name: string; size: number }

export function attachmentNote(files: MessageAttachment[]): string {
  if (files.length > 0 && files.every(f => typeof f.id === 'string' && f.id.length > 0)) {
    return NOTE_PREFIX + JSON.stringify(files.map(f => ({ id: f.id, name: oneLine(f.name), size: f.size })))
  }
  return NOTE_PREFIX + files.map(f => `${oneLine(f.name)} (${formatSize(f.size)})`).join(', ')
}

/** The text to send: what was typed, a blank line, the note. Files alone get a generic first line. */
export function composeMessage(text: string, files: MessageAttachment[]): string {
  if (files.length === 0) return text
  return `${text || GENERIC_LINE}\n\n${attachmentNote(files)}`
}

const ITEM = /(.+?) \((\d+(?:\.\d+)? (?:B|KB|MB))\)(?:, |$)/g

/** Splits a message that ends in the attachment note into its text and the files it names. A
 *  message that does not end in exactly that note is returned as it is. */
export function splitAttachmentNote(text: string): { body: string; files: Array<{ id?: string; name: string; size: string }> } {
  const at = text.lastIndexOf(NOTE_PREFIX)
  const plain = { body: text, files: [] }
  if (at < 0 || (at > 0 && text.slice(at - 2, at) !== '\n\n')) return plain
  const rest = text.slice(at + NOTE_PREFIX.length)
  if (rest.includes('\n') || rest.length > 64 * 1024) return plain
  if (rest.startsWith('[')) {
    try {
      const value: unknown = JSON.parse(rest)
      if (!Array.isArray(value) || value.length === 0 || value.length > MAX_ATTACHMENTS_PER_MESSAGE) return plain
      const identified: Array<{ id: string; name: string; size: string }> = []
      for (const f of value) {
        if (!f || typeof f !== 'object' || typeof f.id !== 'string' || !f.id || f.id.length > 256 ||
          typeof f.name !== 'string' || f.name.length > 1000 || !Number.isSafeInteger(f.size) || f.size < 0) return plain
        identified.push({ id: f.id, name: f.name, size: formatSize(f.size) })
      }
      return { body: text.slice(0, at).trimEnd(), files: identified }
    } catch { return plain }
  }
  const files: Array<{ id?: string; name: string; size: string }> = []
  let consumed = 0
  for (const m of rest.matchAll(ITEM)) {
    if (m.index !== consumed) return plain
    files.push({ name: m[1], size: m[2] })
    consumed = m.index + m[0].length
  }
  if (files.length === 0 || consumed !== rest.length) return plain
  return { body: text.slice(0, at).trimEnd(), files }
}
