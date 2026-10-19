import { useState } from 'react'
import { useEditorState, type Editor } from '@tiptap/react'

type Icon = 'bold' | 'italic' | 'strike' | 'heading' | 'bullet' | 'numbered' | 'quote' | 'code' | 'link' | 'table' | 'undo' | 'redo'
const paths: Record<Icon, string> = {
  bold: 'M6 4h7a4 4 0 0 1 0 8H6m0-8v16h8a4 4 0 0 0 0-8H6',
  italic: 'M10 4h8M6 20h8M14 4l-4 16',
  strike: 'M17 7c-1-4-10-4-10 1 0 2 3 3 5 4M4 12h16M7 17c1 4 10 4 10-1',
  heading: 'M4 4v16M14 4v16M4 12h10M18 15c0-3 5-3 5 0 0 2-5 3-5 6h5',
  bullet: 'M8 6h13M8 12h13M8 18h13M3 6h.1M3 12h.1M3 18h.1',
  numbered: 'M9 6h12M9 12h12M9 18h12M3 3h1v5M3 8h3M2 12c0-3 4-3 4-1 0 2-4 2-4 5h4M2 19h3l-2 2h2',
  quote: 'M9 7H4v6h5V7Zm11 0h-5v6h5V7ZM9 13c0 4-2 5-5 5M20 13c0 4-2 5-5 5',
  code: 'm8 6-6 6 6 6m8-12 6 6-6 6',
  link: 'm10 14 4-4M8 16l-1 1a4 4 0 0 1-6-6l4-4a4 4 0 0 1 6 0m2 1 1-1a4 4 0 0 1 6 6l-4 4a4 4 0 0 1-6 0',
  table: 'M3 3h18v18H3V3Zm0 6h18M3 15h18M9 3v18M15 3v18',
  undo: 'm8 4-5 5 5 5M3 9h11a7 7 0 0 1 0 14',
  redo: 'm16 4 5 5-5 5m5-5H10a7 7 0 0 0 0 14',
}
function FormatIcon({name}: {name: Icon}) {
  return <svg width="19" height="19" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth={name === 'bold' ? 2.6 : 1.8} strokeLinecap="round" strokeLinejoin="round"><path d={paths[name]}/></svg>
}

export default function DocumentToolbar({editor}: {editor: Editor | null}) {
  const active = useEditorState({editor, selector: ({editor: e}) => e ? {
    bold: e.isActive('bold'), italic: e.isActive('italic'), strike: e.isActive('strike'),
    heading: e.isActive('heading', {level: 2}), bullet: e.isActive('bulletList'), numbered: e.isActive('orderedList'),
    quote: e.isActive('blockquote'), code: e.isActive('code'), link: e.isActive('link'), table: e.isActive('table'),
    undo: e.can().undo(), redo: e.can().redo(),
  } : null})
  const [link, setLink] = useState<string | null>(null)
  const [error, setError] = useState('')
  function button(icon: Icon, label: string, action: () => void, toggled?: boolean, disabled = false) {
    return <button type="button" className="document-icon-btn" aria-label={label} title={label} aria-pressed={toggled}
      disabled={!editor || disabled} onMouseDown={event => event.preventDefault()} onClick={action}><FormatIcon name={icon}/></button>
  }
  function saveLink() {
    const href = link?.trim() ?? ''
    if (href.length > 4096 || !/^(https?:\/\/|mailto:)/i.test(href) || [...href].some(char => {const code = char.charCodeAt(0);return code <= 32 || (code >= 127 && code <= 159) || /\s/.test(char)})) {
      setError('Use an https://, http:// or mailto: link without spaces.');return
    }
    if (editor?.chain().focus().extendMarkRange('link').setLink({href}).run()) setLink(null)
  }
  return <>
    <div className="document-format" role="toolbar" aria-label="Document formatting">
      <div role="group" aria-label="Text style" className="document-format-group">
        {button('bold', 'Bold', () => {editor?.chain().focus().toggleBold().run()}, !!active?.bold)}
        {button('italic', 'Italic', () => {editor?.chain().focus().toggleItalic().run()}, !!active?.italic)}
        {button('strike', 'Strikethrough', () => {editor?.chain().focus().toggleStrike().run()}, !!active?.strike)}
        {button('heading', 'Heading', () => {editor?.chain().focus().toggleHeading({level: 2}).run()}, !!active?.heading)}
      </div>
      <div role="group" aria-label="Paragraph format" className="document-format-group">
        {button('bullet', 'Bullet list', () => {editor?.chain().focus().toggleBulletList().run()}, !!active?.bullet)}
        {button('numbered', 'Numbered list', () => {editor?.chain().focus().toggleOrderedList().run()}, !!active?.numbered)}
        {button('quote', 'Quote', () => {editor?.chain().focus().toggleBlockquote().run()}, !!active?.quote)}
        {button('code', 'Code', () => {editor?.chain().focus().toggleCode().run()}, !!active?.code)}
      </div>
      <div role="group" aria-label="Insert" className="document-format-group">
        {button('table', 'Insert table', () => {editor?.chain().focus().insertTable({rows: 3, cols: 3, withHeaderRow: true}).run()}, undefined, !!active?.table)}
        {button('link', 'Link', () => {setError('');setLink(String(editor?.getAttributes('link').href ?? 'https://'))}, !!active?.link)}
      </div>
      <div role="group" aria-label="Edit history" className="document-format-group">
        {button('undo', 'Undo', () => {editor?.chain().focus().undo().run()}, undefined, !active?.undo)}
        {button('redo', 'Redo', () => {editor?.chain().focus().redo().run()}, undefined, !active?.redo)}
      </div>
    </div>
    {link !== null && <form className="document-link-editor" aria-label="Edit link" onSubmit={event => {event.preventDefault();saveLink()}}
      onKeyDown={event => {if (event.key === 'Escape') {event.preventDefault();event.stopPropagation();setLink(null)}}}>
      <label>Link URL<input autoFocus aria-label="Link URL" value={link} onChange={event => setLink(event.target.value)} maxLength={4096}/></label>
      <button type="submit" className="artifact-btn">Apply link</button>
      <button type="button" className="artifact-btn" onClick={() => {editor?.chain().focus().extendMarkRange('link').unsetLink().run();setLink(null)}}>Remove link</button>
      <button type="button" className="artifact-btn" onClick={() => {setLink(null);editor?.commands.focus()}}>Cancel</button>
      {error && <p role="alert">{error}</p>}
    </form>}
  </>
}
