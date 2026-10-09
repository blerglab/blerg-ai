// Files a person attaches to a chat message. Each is uploaded at once by the transport; the
// message that is sent carries a fixed one-line note naming them, which is what makes the agent
// fetch them (`blerg-runner fetch --all`). The same note is read back here to draw the files as
// chips instead of raw text.
import { formatSize } from './artifacts'

export const MAX_ATTACHMENT_BYTES = 25 * 1024 * 1024
export const MAX_ATTACHMENTS_PER_MESSAGE = 10

const NOTE_PREFIX = 'Attached files (fetch them with: blerg-runner fetch --all): '
const GENERIC_LINE = 'Please take a look at the attached files.'

export interface UploadedFile { id: string; name: string; size: number }

/** The name as it reads in the note: one line, whatever the file was called. */
const oneLine = (s: string) => s.replace(/[\r\n\t]+/g, ' ').trim()

export function attachmentNote(files: Array<{ name: string; size: number }>): string {
  return NOTE_PREFIX + files.map(f => `${oneLine(f.name)} (${formatSize(f.size)})`).join(', ')
}

/** The text to send: what was typed, a blank line, the note. Files alone get a generic first line. */
export function composeMessage(text: string, files: Array<{ name: string; size: number }>): string {
  if (files.length === 0) return text
  return `${text || GENERIC_LINE}\n\n${attachmentNote(files)}`
}

const ITEM = /(.+?) \((\d+(?:\.\d+)? (?:B|KB|MB))\)(?:, |$)/g

/** Splits a message that ends in the attachment note into its text and the files it names. A
 *  message that does not end in exactly that note is returned as it is. */
export function splitAttachmentNote(text: string): { body: string; files: Array<{ name: string; size: string }> } {
  const at = text.lastIndexOf(NOTE_PREFIX)
  const plain = { body: text, files: [] }
  if (at < 0 || (at > 0 && text.slice(at - 2, at) !== '\n\n')) return plain
  const rest = text.slice(at + NOTE_PREFIX.length)
  if (rest.includes('\n')) return plain
  const files: Array<{ name: string; size: string }> = []
  let consumed = 0
  for (const m of rest.matchAll(ITEM)) {
    if (m.index !== consumed) return plain
    files.push({ name: m[1], size: m[2] })
    consumed = m.index + m[0].length
  }
  if (files.length === 0 || consumed !== rest.length) return plain
  return { body: text.slice(0, at).trimEnd(), files }
}
