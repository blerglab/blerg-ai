// Hidden characters are shown, not obeyed (card #131, security review N4).
import { describe, expect, it } from 'vitest'
import { visibleText } from './visibleText'

describe('visibleText', () => {
  it('shows controls, bidi and invisible formatting, separators and tag characters as escapes', () => {
    const cases: [string, string][] = [
      ['a‮b', 'a\\u{202E}b'], ['a\u0007b', 'a\\u{0007}b'], ['a b c', 'a\\u{2028}b\\u{2029}c'],
      ['soft­hyphen', 'soft\\u{00AD}hyphen'], ['x\u{E0041}\u{E007F}', 'x\\u{E0041}\\u{E007F}'], ['v️', 'v\\u{FE0F}'],
      ['m᠎e', 'm\\u{180E}e'], ['z​w', 'z\\u{200B}w'],
    ]
    for (const [input, want] of cases) expect(visibleText(input)).toBe(want)
  })

  it('leaves text, newlines, tabs, carriage returns and other astral characters alone', () => {
    expect(visibleText('Line one\n\tLine two\r\nCafé 😀 日本')).toBe('Line one\n\tLine two\r\nCafé 😀 日本')
  })
})
