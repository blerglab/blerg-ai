import { afterEach, describe, expect, it, vi } from 'vitest'
import { mermaidTheme, observeVisualTheme, readVisualTheme, vegaTheme, type VisualTheme } from './theme'

const hosts: HTMLElement[] = []
const disposers: Array<() => void> = []

function host(tokens: Record<string, string> = {}) {
  const element = document.createElement('div')
  const defaults = {
    '--chat-fg': '#eeeeee', '--chat-muted': '#bbbbbb', '--chat-surface': '#171717',
    '--chat-surface-2': '#222222', '--chat-border': '#444444', '--chat-accent': '#ff6633',
    '--chat-font': 'system-ui, sans-serif',
  }
  for (const [name, value] of Object.entries({...defaults, ...tokens})) element.style.setProperty(name, value)
  document.body.append(element)
  hosts.push(element)
  return element
}

function assertInertTheme(theme: VisualTheme) {
  for (const color of [theme.fg, theme.muted, theme.surface, theme.surface2, theme.border, theme.accent, ...theme.palette]) {
    expect(color).toMatch(/^#[0-9a-f]{6}$/i)
  }
  expect(theme.fontFamily).not.toMatch(/[;{}<>\\]|url\s*\(|@import/i)
  expect([...theme.fontFamily].every(char => char.charCodeAt(0) >= 32 && char.charCodeAt(0) <= 126)).toBe(true)
  expect(theme.fontFamily.length).toBeLessThanOrEqual(256)
  expect(Number.isFinite(theme.fontSize)).toBe(true)
  expect(theme.fontSize).toBeGreaterThanOrEqual(8)
  expect(theme.fontSize).toBeLessThanOrEqual(32)
}

afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose()
  for (const element of hosts.splice(0)) element.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('independent native visual theme security', () => {
  it.each([
    'url(https://example.com/track)',
    'url(//example.com/track)',
    'red; background-image:url(https://example.com/track)',
    '#fff;}body{display:none}',
    'expression(alert(1))',
    'var(--missing-token)',
    '<img src=https://example.com/track>',
    'u\\72l(https://example.com/track)',
    'red\u001b[2J',
  ])('never passes active or unresolved color tokens to the renderers: %s', value => {
    const element = host(Object.fromEntries([
      '--chat-fg', '--chat-muted', '--chat-surface', '--chat-surface-2',
      '--chat-border', '--chat-accent', '--chat-code-string', '--chat-code-number',
    ].map(name => [name, value])))
    const theme = readVisualTheme(element)
    assertInertTheme(theme)
    const mapped = JSON.stringify({mermaid: mermaidTheme(theme), vega: vegaTheme(theme)})
    expect(mapped).not.toMatch(/example\.com|expression\s*\(|<img|url\s*\(|@import|\\72l/i)
  })

  it.each([
    'safe; background-image:url(https://example.com/track)',
    'safe";}body{display:none}',
    '@import "https://example.com/font"',
    'url(https://example.com/font)',
    '<style>body{display:none}</style>',
    'font\\00003b family',
    'font\nfamily',
    'font\u0085family',
    'A'.repeat(257),
  ])('keeps unsafe font text out of generated style/config: %s', value => {
    const theme = readVisualTheme(host({'--chat-font': value}))
    assertInertTheme(theme)
    expect(theme.fontFamily).not.toBe(value)
  })

  it('keeps themes scoped to separate chat roots', () => {
    const dark = readVisualTheme(host({'--chat-fg': '#eeeeee', '--chat-surface': '#171717'}))
    const light = readVisualTheme(host({'--chat-fg': '#222222', '--chat-surface': '#f5f5f5'}))
    expect(dark.fg).not.toBe(light.fg)
    expect(dark.surface).not.toBe(light.surface)
    expect(mermaidTheme(dark)).not.toEqual(mermaidTheme(light))
    expect(vegaTheme(dark)).not.toEqual(vegaTheme(light))
  })

  it('keeps independent mappings detached from caller mutation', () => {
    const theme = readVisualTheme(host())
    const first = vegaTheme(theme)
    const before = JSON.stringify(first)
    const other = readVisualTheme(host({'--chat-accent': '#0088cc'}))
    vegaTheme(other)
    mermaidTheme(other)
    expect(JSON.stringify(first)).toBe(before)
  })
})

describe('independent theme observer lifecycle security', () => {
  function frames() {
    let next = 1
    const pending = new Map<number, FrameRequestCallback>()
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      const id = next++
      pending.set(id, callback)
      return id
    })
    vi.stubGlobal('cancelAnimationFrame', (id: number) => pending.delete(id))
    return {
      pending,
      async flush() {
        await Promise.resolve()
        await Promise.resolve()
        const work = [...pending.values()]
        pending.clear()
        for (const callback of work) callback(performance.now())
        await Promise.resolve()
      },
    }
  }

  it('does not watch renderer descendants or feed rendering mutations back into theme updates', async () => {
    const clock = frames()
    const element = host()
    const changed = vi.fn()
    disposers.push(observeVisualTheme(element, changed))
    const canvas = document.createElement('canvas')
    element.append(canvas)
    canvas.style.width = '300px'
    canvas.setAttribute('class', 'marks')
    await clock.flush()
    expect(changed).not.toHaveBeenCalled()
    expect(clock.pending.size).toBe(0)
  })

  it('coalesces a burst of token changes and stops callbacks after disposal', async () => {
    const clock = frames()
    const element = host()
    const changed = vi.fn()
    const dispose = observeVisualTheme(element, changed)
    disposers.push(dispose)
    element.style.setProperty('--chat-fg', '#ffffff')
    element.style.setProperty('--chat-fg', '#dddddd')
    element.style.setProperty('--chat-fg', '#cccccc')
    await clock.flush()
    expect(changed).toHaveBeenCalledTimes(1)
    expect(changed.mock.calls[0][0].fg).toBe('#cccccc')
    element.style.setProperty('--chat-fg', '#aaaaaa')
    await Promise.resolve()
    dispose()
    await clock.flush()
    expect(changed).toHaveBeenCalledTimes(1)
    expect(clock.pending.size).toBe(0)
  })
})
