import { useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import Markdown from '../Markdown'
import './comment-editor.css'

type Format = 'bold' | 'italic' | 'code' | 'link' | 'heading' | 'quote' | 'bullet' | 'number'
interface Range { start: number; end: number }
interface Edit extends Range { value: string }

function wrap(value: string, range: Range, before: string, after = before, placeholder = 'text'): Edit {
  const {start, end} = range
  const selected = value.slice(start, end)
  if (selected && value.slice(start - before.length, start) === before && value.slice(end, end + after.length) === after) {
    return {value: value.slice(0, start - before.length) + selected + value.slice(end + after.length),
      start: start - before.length, end: end - before.length}
  }
  const text = selected || placeholder
  return {value: value.slice(0, start) + before + text + after + value.slice(end),
    start: start + before.length, end: start + before.length + text.length}
}

function format(value: string, range: Range, kind: Format): Edit {
  const selected = value.slice(range.start, range.end)
  if (kind === 'bold') return wrap(value, range, '**')
  if (kind === 'italic') return wrap(value, range, '_')
  if (kind === 'link') {
    const text = selected || 'link text'
    const prefix = `[${text}](`
    const destination = 'https://'
    return {value: value.slice(0, range.start) + prefix + destination + ')' + value.slice(range.end),
      start: range.start + prefix.length, end: range.start + prefix.length + destination.length}
  }
  if (kind === 'code') {
    const longest = Math.max(0, ...(selected.match(/`+/g) ?? []).map(run => run.length))
    if (selected.includes('\n')) {
      const fence = '`'.repeat(Math.max(3, longest + 1))
      const before = (range.start > 0 && value[range.start - 1] !== '\n' ? '\n' : '') + fence + '\n'
      const after = '\n' + fence + (range.end < value.length && value[range.end] !== '\n' ? '\n' : '')
      return wrap(value, range, before, after, 'code')
    }
    const fence = '`'.repeat(longest + 1)
    const pad = /^`|`$/.test(selected) ? ' ' : ''
    return wrap(value, range, fence + pad, pad + fence, 'code')
  }
  const from = range.start === 0 ? 0 : value.lastIndexOf('\n', range.start - 1) + 1
  const selectedEnd = range.end > range.start && value[range.end - 1] === '\n' ? range.end - 1 : range.end
  const next = value.indexOf('\n', selectedEnd)
  const to = next === -1 ? value.length : next
  const lines = value.slice(from, to).split('\n')
  const match = kind === 'heading' ? /^#{1,6}\s+/ : kind === 'quote' ? /^>\s?/ : kind === 'bullet' ? /^[-*+]\s+/ : /^\d+\.\s+/
  const remove = lines.every(line => match.test(line))
  let firstDelta = 0
  const changed = lines.map((line, index) => {
    const prefix = kind === 'heading' ? '### ' : kind === 'quote' ? '> ' : kind === 'bullet' ? '- ' : `${index + 1}. `
    const result = remove ? line.replace(match, '') : prefix + line
    if (index === 0) firstDelta = result.length - line.length
    return result
  }).join('\n')
  const last = range.end > to ? 0 : to - range.end
  return {value: value.slice(0, from) + changed + value.slice(to),
    start: Math.max(from, range.start + firstDelta), end: from + changed.length - last}
}

const TOOLS: readonly {kind: Format; label: string; text: string; title: string}[] = [
  {kind: 'heading', label: 'Heading', text: 'H', title: 'Heading'},
  {kind: 'bold', label: 'Bold', text: 'B', title: 'Bold (Ctrl/⌘ B)'},
  {kind: 'italic', label: 'Italic', text: 'I', title: 'Italic (Ctrl/⌘ I)'},
  {kind: 'code', label: 'Code', text: '<>', title: 'Code (Ctrl/⌘ E)'},
  {kind: 'link', label: 'Link', text: '↗', title: 'Link (Ctrl/⌘ K)'},
  {kind: 'quote', label: 'Quote', text: '❞', title: 'Quote'},
  {kind: 'bullet', label: 'Bulleted list', text: '• ≡', title: 'Bulleted list'},
  {kind: 'number', label: 'Numbered list', text: '1. ≡', title: 'Numbered list'},
]

export interface CommentEditorProps {
  value: string
  onChange: (value: string) => void
  label?: string
  autoFocus?: boolean
  /** The parent binds a structured suggestion to the current new-side selection. */
  onSuggest?: () => void
}

export default function CommentEditor({value, onChange, label = 'Comment', autoFocus = false, onSuggest}: CommentEditorProps) {
  const id = useId()
  const textarea = useRef<HTMLTextAreaElement>(null)
  const writeTab = useRef<HTMLButtonElement>(null)
  const previewTab = useRef<HTMLButtonElement>(null)
  const selected = useRef<Range | null>(null)
  const pending = useRef<Range | null>(null)
  const [preview, setPreview] = useState(false)
  useLayoutEffect(() => {
    const target = textarea.current
    const range = pending.current ?? selected.current
    if (!preview && target && range) {
      if (pending.current) target.focus()
      target.setSelectionRange(Math.min(range.start, value.length), Math.min(range.end, value.length))
      selected.current = range
      pending.current = null
    }
  }, [value, preview])

  function remember() {
    const target = textarea.current
    if (target) selected.current = {start: target.selectionStart, end: target.selectionEnd}
  }
  function apply(kind: Format) {
    const target = textarea.current
    if (!target) return
    const edit = format(value, {start: target.selectionStart, end: target.selectionEnd}, kind)
    const start = Math.max(0, Math.min(edit.start, edit.value.length))
    pending.current = {start, end: Math.max(start, Math.min(edit.end, edit.value.length))}
    onChange(edit.value)
  }
  function shortcut(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (!(event.ctrlKey || event.metaKey) || event.altKey) return
    const command: Partial<Record<string, Format>> = {b: 'bold', i: 'italic', k: 'link', e: 'code'}
    const kind = command[event.key.toLowerCase()]
    if (kind) {event.preventDefault(); apply(kind)}
  }
  function switchTab(event: KeyboardEvent<HTMLButtonElement>) {
    const keys = ['ArrowLeft', 'ArrowRight', 'Home', 'End']
    if (!keys.includes(event.key)) return
    event.preventDefault(); remember()
    const next = event.key === 'Home' ? false : event.key === 'End' ? true : !preview
    setPreview(next)
    ;(next ? previewTab : writeTab).current?.focus()
  }

  return <div className="comment-editor" data-palette-shortcuts="ignore">
    <div className="comment-editor-header">
      <div className="comment-editor-tabs" role="tablist" aria-label={`${label} mode`}>
        <button type="button" role="tab" id={`${id}-write`} aria-controls={`${id}-panel`} aria-selected={!preview}
          tabIndex={preview ? -1 : 0} ref={writeTab} onKeyDown={switchTab} onClick={() => setPreview(false)}>Write</button>
        <button type="button" role="tab" id={`${id}-preview`} aria-controls={`${id}-panel`} aria-selected={preview}
          tabIndex={preview ? 0 : -1} ref={previewTab} onKeyDown={switchTab} onClick={() => {remember(); setPreview(true)}}>Preview</button>
      </div>
      {!preview && <div className="comment-editor-tools" role="toolbar" aria-label={`${label} formatting`}>
        {TOOLS.map(tool => <button key={tool.kind} type="button" aria-label={tool.label} title={tool.title}
          onMouseDown={event => event.preventDefault()} onClick={() => apply(tool.kind)}>{tool.text}</button>)}
        {onSuggest && <button type="button" aria-label="Insert suggestion" title="Insert suggestion" onClick={onSuggest}>±</button>}
      </div>}
    </div>
    <div className="comment-editor-panel" id={`${id}-panel`} role="tabpanel" aria-labelledby={`${id}-${preview ? 'preview' : 'write'}`}>
      {preview ? <div className="comment-editor-preview">{value.trim() ? <Markdown text={value}/> : <p className="comment-editor-empty">Nothing to preview.</p>}</div>
        : <textarea ref={textarea} value={value} aria-label={label} autoFocus={autoFocus} rows={5}
          placeholder="Leave a comment" onKeyDown={shortcut} onSelect={remember}
          onChange={event => {selected.current = {start: event.target.selectionStart, end: event.target.selectionEnd}; onChange(event.target.value)}}/>}
    </div>
    <p className="comment-editor-hint">Markdown is supported.</p>
  </div>
}
