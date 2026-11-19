import { describe, expect, it } from 'vitest'
import { decodeLabel } from './label'

describe('decodeLabel', () => {
  it('passes plain text through unchanged', () => {
    expect(decodeLabel('Hard')).toBe('Hard')
  })
  it('decodes standard named and numeric entities', () => {
    expect(decodeLabel('Amp &amp; quote &quot;')).toBe('Amp & quote "')
    expect(decodeLabel('&#38; and &#x26;')).toBe('& and &')
  })
  it('turns <br> and <br/> into a line break', () => {
    expect(decodeLabel('First<br/>Second')).toBe('First\nSecond')
    expect(decodeLabel('First<br>Second')).toBe('First\nSecond')
  })
  it('falls back (null) on any other angle-bracket markup', () => {
    expect(decodeLabel('<b>bold</b>')).toBeNull()
    expect(decodeLabel('a < b')).toBeNull()
  })
  it('falls back (null) on legacy ampersand-less entity shorthand', () => {
    expect(decodeLabel('colon: #quot;quoted#quot;')).toBeNull()
  })
  it('leaves a bare & and unknown named entities as literal text', () => {
    expect(decodeLabel('100% complete & ready')).toBe('100% complete & ready')
    expect(decodeLabel('&notareal;')).toBe('&notareal;')
  })
  it('keeps unicode text as-is', () => {
    expect(decodeLabel('Résumé café Ω 東京 🚀')).toBe('Résumé café Ω 東京 🚀')
  })
})
