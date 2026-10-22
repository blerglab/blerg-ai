// A schema-constrained Markdown editor. Track decorations never enter the document or upload.
import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { EditorContent, useEditor } from '@tiptap/react'
import { Extension } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import { Markdown } from '@tiptap/markdown'
import { TableKit } from '@tiptap/extension-table'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import type { Node } from '@tiptap/pm/model'
import { findQuote } from '../../model/anchor'
import type { Anchor, ReviewRequest } from '../../model/review'
import DocumentToolbar from './DocumentToolbar'
import { wordDiff } from '../../model/diff'

// These constructs cannot safely roundtrip through the rich schema. Keep their source intact.
export function richDocumentSupported(source: string): boolean {
  return source.length <= 200_000 && !/(^---\s*\n)|<\?|<[/?]?[a-z!]|!\[|^\s*\[[^\]]+\]:|\]\[[^\]]*\]|\[\^|^\s*```(?:mermaid|vega-lite)|\$\$|^\s*~~~(?:mermaid|vega-lite)/im.test(source)
}

function textMap(doc: Node) {
  let text = ''
  const positions: number[] = []
  doc.descendants((node, pos) => {
    if (node.isText && node.text) {
      for (let i = 0; i < node.text.length; i++) { positions.push(pos + i); text += node.text[i] }
    } else if (node.isTextblock && text) { positions.push(pos); text += '\n' }
  })
  positions.push(Math.max(0, doc.content.size - 1))
  return {text, positions}
}

function structure(doc: Node): string {
  return JSON.stringify(doc.toJSON(), (key, value) => key === 'text' ? undefined : value)
}

const trackingKey = new PluginKey('document-review-tracking')
function tracking(baseline: string, initialShow: boolean) {
  return Extension.create({
    name: 'reviewTracking',
    addProseMirrorPlugins() {
      return [new Plugin({key: trackingKey, state: {init: () => initialShow, apply: (tr, previous) => tr.getMeta(trackingKey) ?? previous}, props: {decorations(state) {
        if (!trackingKey.getState(state)) return DecorationSet.empty
        const {text, positions} = textMap(state.doc)
        const marks: Decoration[] = []
        let offset = 0
        for (const part of wordDiff(baseline, text)) {
          if (part.kind === 'del') {
            marks.push(Decoration.widget(positions[offset] ?? 0, () => {
              const span = document.createElement('del')
              span.className = 'document-deletion'
              span.dataset.reviewDeletion = 'true'
              span.contentEditable = 'false'
              span.setAttribute('aria-label', `Deleted: ${part.text}`)
              span.textContent = part.text
              return span
            }, {side: -1, ignoreSelection: true}))
          } else {
            if (part.kind === 'ins') {
              const from = positions[offset], to = positions[offset + part.text.length - 1]
              if (from !== undefined && to !== undefined && from < to + 1) marks.push(Decoration.inline(from, to + 1, {class: 'document-insertion'}))
            }
            offset += part.text.length
          }
        }
        return DecorationSet.create(state.doc, marks)
      }}})]
    },
  })
}

interface Props {
  value: string
  published: string
  showChanges: boolean
  onChange(value: string): void
  onInvalid?(ids: string[]): void
  invalidIds?: ReadonlySet<string>
  eligibleIds?: ReadonlySet<string>
  requests?: ReviewRequest[]
  resolveRef?: RefObject<((id: string, anchor: Anchor) => Range | null) | null>
  rootRef: RefObject<HTMLDivElement | null>
}

export default function DocumentEditor({value, published, showChanges, onChange, rootRef, requests = [], invalidIds, eligibleIds, resolveRef, onInvalid}: Props) {
  const initial = useRef(value)
  const [formatChanged, setFormatChanged] = useState(false)
  const baselineStructure = useRef('')
  const bookmarks = useRef(new Map<string, {from: number; to: number; quote: string; valid: boolean}>())
  const canonical = useRef<string | null>(null)
  const lastEmitted = useRef(value)
  const onInvalidRef = useRef(onInvalid)
  useEffect(() => { onInvalidRef.current = onInvalid }, [onInvalid])
  const onChangeRef = useRef(onChange)
  useEffect(() => { onChangeRef.current = onChange }, [onChange])
  const editor = useEditor({
    extensions: [StarterKit.configure({trailingNode: false, link: {openOnClick: false, autolink: false, linkOnPaste: false,
      isAllowedUri: uri => /^(https?:\/\/|mailto:)/i.test(uri.trim())}}), Markdown, TableKit, TaskList, TaskItem.configure({nested: true}),
      Extension.create({name: 'trackingBaseline', addProseMirrorPlugins() {
        // Parse the published copy with the same schema, without inserting it into the editor.
        const json = this.editor.markdown?.parse(published)
        const doc = json ? this.editor.schema.nodeFromJSON(json) : this.editor.state.doc
        return tracking(textMap(doc).text, showChanges).config.addProseMirrorPlugins?.call(this) ?? []
      }})],
    content: value,
    contentType: 'markdown',
    immediatelyRender: true,
    editorProps: {
      attributes: {'aria-label': 'Document', role: 'textbox', 'aria-multiline': 'true', class: 'document-editor md md-doc md-box'},
      handleDOMEvents: {
        drop: (_view, event) => {event.preventDefault(); return true},
        paste: (view, event) => {
          event.preventDefault()
          const text = event.clipboardData?.getData('text/plain') ?? ''
          if (text) view.dispatch(view.state.tr.insertText(text))
          return true
        },
      },
      handlePaste(view, event) {
        const text = event.clipboardData?.getData('text/plain')
        if (text === undefined) return true
        event.preventDefault()
        view.dispatch(view.state.tr.insertText(text))
        return true
      },
      handleDrop: (_view, event) => {event.preventDefault(); return true},
    },
    onTransaction({transaction: tr, editor: e}) {
      if (!tr.docChanged) return
      for (const [id, bookmark] of bookmarks.current) {
        if (!bookmark.valid) continue
        const from = tr.mapping.mapResult(bookmark.from, 1)
        const to = tr.mapping.mapResult(bookmark.to, -1)
        bookmark.from = from.pos; bookmark.to = to.pos
        bookmark.valid = !from.deletedAcross && !to.deletedAcross && from.pos < to.pos &&
          e.state.doc.textBetween(from.pos, to.pos, ' ').replace(/\s+/g, ' ').trim() === bookmark.quote
        if (!bookmark.valid) onInvalidRef.current?.([id])
      }
    },
    onCreate({editor: e}) { canonical.current = e.getMarkdown(); baselineStructure.current = structure(e.state.doc) },
    onUpdate({editor: e}) {
      setFormatChanged(structure(e.state.doc) !== baselineStructure.current)
      const next = e.getMarkdown()
      const source = next === canonical.current ? initial.current : next
      lastEmitted.current = source
      onChangeRef.current(source)
    },
  }, [])
  useLayoutEffect(() => {
    if (!editor) return
    const normalized = (s: string) => s.replace(/\s+/g, ' ').trim()
    for (const r of requests) {
      if (bookmarks.current.has(r.id)) continue
      const range = findQuote(editor.view.dom, r.anchor)
      const quote = normalized(r.anchor.quote ?? '')
      // A remount after source edits cannot reconstruct transaction history. Refuse ambiguous
      // matches rather than silently attaching an old comment to another identical passage.
      const valid = !!range && !invalidIds?.has(r.id) && !!eligibleIds?.has(r.id)
      if (!valid) onInvalidRef.current?.([r.id])
      bookmarks.current.set(r.id, {from: range ? editor.view.posAtDOM(range.startContainer, range.startOffset) : 0,
        to: range ? editor.view.posAtDOM(range.endContainer, range.endOffset) : 0, quote, valid})
    }
    if (!resolveRef) return
    resolveRef.current = (id) => {
      const b = bookmarks.current.get(id)
      if (!b?.valid || editor.isDestroyed) return null
      const from = editor.view.domAtPos(b.from), to = editor.view.domAtPos(b.to)
      const range = document.createRange()
      range.setStart(from.node, from.offset);range.setEnd(to.node, to.offset)
      return range
    }
    return () => {resolveRef.current = null}
  }, [editor, requests, published, resolveRef, invalidIds, eligibleIds])
  useEffect(() => {
    if (!editor || value === lastEmitted.current) return
    editor.commands.setContent(value, {contentType: 'markdown', emitUpdate: false})
    lastEmitted.current = value
  }, [editor, value])
  useEffect(() => {
    if (editor) editor.view.dispatch(editor.state.tr.setMeta(trackingKey, showChanges))
  }, [editor, showChanges])
  return <>
    <DocumentToolbar editor={editor}/>
    {showChanges && formatChanged && <p role="status" className="review-notice">Formatting or structure changes are included in the submitted source diff.</p>}
    <div className="review-page document-page" data-testid="review-page" ref={rootRef}><EditorContent editor={editor}/></div>
  </>
}
