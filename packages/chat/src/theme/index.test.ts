import { describe, expect, it } from 'vitest'
import { TOKEN_NAMES, themeStyle } from './index'

describe('themeStyle', () => {
  it('turns a tokens object into the custom properties the stylesheets read', () => {
    expect(themeStyle({ bg: '#000', accentFg: 'white', cardUserBg: 'pink', textXs: '11px' })).toEqual({
      '--chat-bg': '#000',
      '--chat-accent-fg': 'white',
      '--chat-card-user-bg': 'pink',
      '--chat-text-xs': '11px',
    })
  })
  it('leaves out what is not set, so the app stylesheet or the fallback decides', () => {
    expect(themeStyle({})).toEqual({})
    expect(themeStyle({ bg: undefined })).toEqual({})
  })
  it('names every token of the contract', () => {
    expect(Object.values(TOKEN_NAMES).sort()).toEqual([
      '--chat-accent', '--chat-accent-fg', '--chat-bg', '--chat-border',
      '--chat-card-assistant-bg', '--chat-card-system-bg', '--chat-card-tool-bg', '--chat-card-user-bg',
      '--chat-danger', '--chat-density', '--chat-fg', '--chat-font', '--chat-font-mono', '--chat-info',
      '--chat-muted', '--chat-muted-2', '--chat-radius', '--chat-success', '--chat-surface', '--chat-surface-2',
      '--chat-text-sm', '--chat-text-xs', '--chat-warning',
    ])
  })
})
