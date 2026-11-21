// BLG-468 design doc §2 label rule: entities decoded, a literal <br>/<br/> becomes a line break,
// anything else that looks like markup (any other angle bracket, or Mermaid's own legacy
// ampersand-less entity shorthand like #quot;) is unsupported — the caller falls back to Mermaid
// rather than draw an approximation. Pure and DOM-free on purpose (no innerHTML trick): a decoder
// that interprets arbitrary HTML to decode entities would also silently interpret the very markup
// this is meant to reject.

const NAMED_ENTITY: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' }

function decodeEntities(text: string): string {
  return text.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (whole, body: string) => {
    if (body[0] === '#') {
      const code = /^#x/i.test(body) ? Number.parseInt(body.slice(2), 16) : Number.parseInt(body.slice(1), 10)
      return Number.isFinite(code) ? String.fromCodePoint(code) : whole
    }
    return NAMED_ENTITY[body.toLowerCase()] ?? whole
  })
}

/** `null` means the label is unsupported — the whole diagram falls back (the failure rule),
 *  never a label drawn with stray markup left in it. */
export function decodeLabel(raw: string): string | null {
  // Mermaid's backtick-fenced "markdown string" labels (bold/italic/multi-line markdown) are a
  // different, richer label grammar than plain text — unsupported here, same as any other markup.
  if (/^`[\s\S]*`$/.test(raw.trim())) return null
  const withBreaks = raw.replace(/<br\s*\/?>/gi, '\n')
  if (/[<>]/.test(withBreaks)) return null
  // Mermaid's own legacy entity shorthand (#quot; etc., no leading &) is a different escape than
  // the standard &...; form decodeEntities handles — reject rather than guess its named-entity
  // table. The lookbehind excludes a real &#38;-style numeric entity, which also contains #38;.
  if (/(?<!&)#(?:x[0-9a-f]+|\d+|[a-z]+);/i.test(withBreaks)) return null
  return decodeEntities(withBreaks)
}
