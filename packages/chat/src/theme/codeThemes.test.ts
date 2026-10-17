import { describe, expect, it } from 'vitest'
import {
  CODE_THEMES, CODE_TOKEN_KEYS, CODE_TOKEN_NAMES, DEFAULT_CODE_COLOURS, applyCodeColours, applyCodeTheme, codeColourVars,
  codeThemes, isHexColour, normaliseCodeColours, resolveCodeTokens, validateCodeColours,
} from './codeThemes'

describe('codeThemes', () => {
  it('lists the ten themes, Blerg first', () => {
    expect(CODE_THEMES.map(x => x.id)).toEqual([
      'blerg', 'github-dark', 'github-light', 'one-dark', 'dracula', 'monokai', 'solarized-dark', 'solarized-light', 'nord', 'gruvbox-dark',
    ])
  })
  it('gives every theme a value for every token', () => {
    for (const th of CODE_THEMES) {
      for (const variant of [th.tokens, th.light].filter(Boolean)) {
        for (const k of CODE_TOKEN_KEYS) expect(variant![k], `${th.id}.${k}`).toBeTruthy()
      }
    }
  })
  it('uses plain #rrggbb in every theme but Blerg, which follows the app palette', () => {
    for (const th of CODE_THEMES.filter(x => x.id !== 'blerg')) {
      for (const k of CODE_TOKEN_KEYS) expect(isHexColour(th.tokens[k]), `${th.id}.${k}`).toBe(true)
    }
  })
  it('applyCodeTheme sets every custom property on the element', () => {
    const el = document.createElement('div')
    applyCodeTheme(el, 'nord', 'dark')
    for (const k of CODE_TOKEN_KEYS) expect(el.style.getPropertyValue(CODE_TOKEN_NAMES[k])).toBe(codeThemes.nord.tokens[k])
  })
  it('lays overrides over the theme and ignores one that is not #rrggbb', () => {
    const r = resolveCodeTokens('dracula', 'dark', { keyword: '#112233', string: 'red' })
    expect(r.keyword).toBe('#112233')
    expect(r.string).toBe(codeThemes.dracula.tokens.string)
  })
  it('Blerg has its own light colours; another theme is the same in both modes', () => {
    expect(resolveCodeTokens('blerg', 'light').keyword).not.toBe(resolveCodeTokens('blerg', 'dark').keyword)
    expect(resolveCodeTokens('monokai', 'light')).toEqual(resolveCodeTokens('monokai', 'dark'))
  })
  it('applies the choice for the asked mode', () => {
    const el = document.createElement('div')
    applyCodeColours(el, { dark: 'nord', light: 'github-light', overrides: { dark: {}, light: { bg: '#fafafa' } } }, 'light')
    expect(el.style.getPropertyValue('--chat-code-bg')).toBe('#fafafa')
    expect(el.style.getPropertyValue('--chat-code-keyword')).toBe(codeThemes['github-light'].tokens.keyword)
    expect(codeColourVars(DEFAULT_CODE_COLOURS, 'dark')['--chat-code-fg']).toBe('var(--chat-fg)')
  })
  it('validates strictly and normalises leniently', () => {
    expect(validateCodeColours(DEFAULT_CODE_COLOURS)).toBe('')
    expect(validateCodeColours({ dark: 'nope', light: 'blerg' })).toMatch(/unknown theme/)
    expect(validateCodeColours({ dark: 'blerg', light: 'blerg', overrides: { dark: { keyword: 'url(x)' } } })).toMatch(/#rrggbb/)
    expect(validateCodeColours({ dark: 'blerg', light: 'blerg', overrides: { dark: { sparkle: '#ffffff' } } })).toMatch(/unknown token/)
    expect(normaliseCodeColours({ dark: 'nope', light: 'nord', overrides: { light: { keyword: '#abcdef', string: 'x' } } })).toEqual({
      dark: 'blerg', light: 'nord', overrides: { dark: {}, light: { keyword: '#abcdef' } },
    })
    expect(normaliseCodeColours(null)).toEqual(DEFAULT_CODE_COLOURS)
  })
})
