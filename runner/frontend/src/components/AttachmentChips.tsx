// The pieces of the chat's file attachments: the chips under the composer (upload state, remove),
// and a message's text with its attachment note drawn as file chips.
import { formatSize } from '../lib/artifacts'
import { splitAttachmentNote } from '../lib/attachments'
import type { AttachmentItem } from '../hooks/useAttachments'
import Markdown from './Markdown'
import './Attachments.css'

function uploadPercent(i: AttachmentItem): number {
  if (!i.size || i.loaded === undefined) return 0
  return Math.max(0, Math.min(100, Math.round((i.loaded / i.size) * 100)))
}

function uploadText(i: AttachmentItem): string {
  const pct = uploadPercent(i)
  // all bytes are sent but the server has not answered yet
  if (pct >= 100) return 'Processing…'
  return `Uploading ${pct}% · ${formatSize(i.loaded ?? 0)} of ${formatSize(i.size)}`
}

export function ComposerChips({ items, onRemove }: { items: AttachmentItem[]; onRemove: (key: string) => void }) {
  if (items.length === 0) return null
  return (
    <ul className="attach-chips" aria-label="Attached files">
      {items.map(i => (
        <li key={i.key} className={`attach-chip ${i.status}`} data-testid="attach-chip">
          <span className="attach-chip-icon" aria-hidden="true">📎</span>
          <span className="attach-chip-name" title={i.name}>{i.name}</span>
          <span className="attach-chip-meta">
            {i.status === 'uploading' ? uploadText(i) : i.status === 'error' ? 'Not attached' : formatSize(i.size)}
          </span>
          {i.status === 'uploading' && (
            <span
              className="attach-chip-progress"
              role="progressbar"
              aria-label={`Uploading ${i.name}`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={uploadPercent(i)}
            >
              <span className="attach-chip-progress-bar" style={{ width: `${uploadPercent(i)}%` }} />
            </span>
          )}
          <button
            type="button"
            className="attach-chip-remove"
            aria-label={`Remove ${i.name}`}
            onClick={() => onRemove(i.key)}
          >
            ✕
          </button>
          {i.status === 'error' && i.error && <span className="attach-chip-error" role="alert">{i.error}</span>}
        </li>
      ))}
    </ul>
  )
}

/** A message's text, with a trailing attachment note shown as chips instead of raw text. */
export function MessageText({ text }: { text: string }) {
  const { body, files } = splitAttachmentNote(text)
  if (files.length === 0) return <Markdown text={text} />
  return (
    <>
      {body && <Markdown text={body} />}
      <ul className="attach-chips in-message" data-testid="message-attachments" aria-label="Attached files">
        {files.map((f, n) => (
          <li key={n} className="attach-chip done">
            <span className="attach-chip-icon" aria-hidden="true">📎</span>
            <span className="attach-chip-name" title={f.name}>{f.name}</span>
            <span className="attach-chip-meta">{f.size}</span>
          </li>
        ))}
      </ul>
    </>
  )
}
