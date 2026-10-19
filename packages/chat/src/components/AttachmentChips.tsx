// The pieces of the chat's file attachments: the chips under the composer (upload state, remove),
// and a message's text with its attachment note drawn as file chips.
import { useState } from 'react'
import { useChat } from './context'
import { CAP_SHOW, countLines, firstLines, messageNeedsCap } from '../model/lineCap'
import CodeViewer from './CodeViewer'
import { formatSize } from '../model/artifacts'
import { previewKind } from '../model/artifactPreview'
import { splitAttachmentNote } from '../model/attachments'
import type { AttachmentItem } from '../hooks/useAttachments'
import { MediaPreview } from './ArtifactCard'
import ErrorId from './ErrorId'
import Markdown from './Markdown'
import type { CardRegistry } from '../cards/registry'

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
    <ul className="attach-chips composer-attachments" aria-label="Attached files">
      {items.map(i => (
        <li key={i.key} className={`attach-chip composer-attachment ${i.status}${i.preview ? ' has-preview' : ''}`} data-testid="attach-chip">
          <div className={`attach-chip-preview${i.preview ? '' : ' is-file'}`}>
            {!i.preview ? <span className="attach-chip-filetype" aria-hidden="true">{i.name.includes('.') ? i.name.split('.').pop()?.slice(0, 8).toUpperCase() : 'FILE'}</span>
              : i.preview.kind === 'text' ? <span className="attach-chip-excerpt">{i.preview.text}</span>
                : <img src={i.preview.src} alt={`${i.preview.kind === 'pdf' ? 'First page of' : 'Preview of'} ${i.name}`} />}
          </div>
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
          {i.status === 'error' && i.error && <span className="attach-chip-error" role="alert">{i.error}<ErrorId id={i.errorId} /></span>}
        </li>
      ))}
    </ul>
  )
}

/** A person's message as Markdown; one over the line cap (a pasted log, a file) shows its first
 *  lines with "Show all N lines" and "Open". Short messages are untouched. (A message that
 *  renders as the collapsed session brief never comes through here.) */
function CappedMarkdown({ text, cards }: { text: string; cards?: CardRegistry }) {
  const [expanded, setExpanded] = useState(false)
  const [open, setOpen] = useState(false)
  if (!messageNeedsCap(text)) return <Markdown text={text} cards={cards} />
  const collapsed = !expanded
  const total = countLines(text)
  return (
    <div className={`md-capped${collapsed ? ' is-capped' : ''}`} data-testid="message-capped">
      <Markdown text={collapsed ? firstLines(text, CAP_SHOW) : text} cards={cards} />
      <div className="md-codeblock-actions">
        <button type="button" className="md-codeblock-more" aria-expanded={!collapsed} onClick={() => setExpanded(!expanded)}>
          {collapsed ? `Show all ${total} lines` : 'Show less'}
        </button>
        <button type="button" className="md-codeblock-more" onClick={() => setOpen(true)}>Open</button>
      </div>
      {open && <CodeViewer text={text} lang={null} title="Message" onClose={() => setOpen(false)} />}
    </div>
  )
}

/** A message's text, with a trailing attachment note shown as chips instead of raw text, and an
 *  inline preview for each attached image, video or audio file the session's Files list knows. */
export function MessageText({ text, cards }: { text: string; cards?: CardRegistry }) {
  const chat = useChat()
  const { body, files } = splitAttachmentNote(text)
  if (files.length === 0) return <CappedMarkdown text={text} cards={cards} />
  const previews = chat?.files
    ? files.map(f => chat.byName(f.name)).filter((a): a is NonNullable<typeof a> => !!a && previewKind(a.view, a.size) !== null)
    : []
  return (
    <>
      {body && <CappedMarkdown text={body} cards={cards} />}
      {previews.length > 0 && chat?.files && (
        <div className="attach-previews" data-testid="message-attachment-previews">
          {previews.map(a => (
            <MediaPreview key={a.id} sessionId={chat.sessionId} files={chat.files!} artifact={a} onOpen={autoplay => chat.view(a, { autoplay })} />
          ))}
        </div>
      )}
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
