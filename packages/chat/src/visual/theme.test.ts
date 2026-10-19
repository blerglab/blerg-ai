import { afterEach, describe, expect, it, vi } from 'vitest'
import { mermaidTheme, observeVisualTheme, readVisualTheme, vegaTheme, type VisualTheme } from './theme'

const theme: VisualTheme = {fg: '#edf1f7', muted: '#c1cad6', surface: '#131923', surface2: '#1c2430', border: '#405066',
  accent: '#e5865c', fontFamily: '"Example Sans", sans-serif', fontSize: 14,
  palette: ['#e5865c', '#bc9cf0', '#8fc9a0', '#e0b36a', '#7fb2ee', '#6fc7c7'], dark: true}
afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('native visual theme', () => {
  it('maps Mermaid nodes and all required diagram families to native chat tokens', () => {
    expect(mermaidTheme(theme)).toMatchObject({theme: 'base', themeVariables: {
      background: 'transparent', darkMode: true, primaryColor: theme.surface2, primaryTextColor: theme.fg,
      primaryBorderColor: theme.border, lineColor: theme.muted, edgeLabelBackground: theme.surface,
      fontFamily: theme.fontFamily, fontSize: '14px', actorBkg: theme.surface2, actorTextColor: theme.fg,
      signalTextColor: theme.fg, stateBkg: theme.surface2, stateLabelColor: theme.fg,
      mainBkg: theme.surface2, nodeBorder: theme.border, relationColor: theme.muted,
      taskBkgColor: theme.surface2, taskTextColor: theme.fg, taskTextOutsideColor: theme.fg,
    }})
  })
  it('maps Vega labels, grid, mark, categorical palette and font without a white plot', () => {
    expect(vegaTheme(theme)).toMatchObject({background: 'transparent', font: theme.fontFamily,
      view: {stroke: null}, mark: {color: theme.accent},
      axis: {labelColor: theme.muted, titleColor: theme.fg, labelFontSize: 14, gridColor: theme.border},
      legend: {labelColor: theme.fg, titleColor: theme.fg}, title: {color: theme.fg},
      range: {category: theme.palette}})
  })
  it('uses trusted Vega defaults while explicit author mark and encoding colors win', async () => {
    // Vega's compiler probes text measurement; jsdom has no canvas context.
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
    const {compile} = await import('vega-lite')
    const data = {values: [{name: 'A', count: 1}]}
    const encoding = {x: {field: 'name', type: 'nominal' as const}, y: {field: 'count', type: 'quantitative' as const}}
    for (const [mark, color, expected] of [
      ['bar', undefined, theme.accent],
      [{type: 'bar' as const, color: '#123abc'}, undefined, '#123abc'],
      ['bar', {value: '#654321'}, '#654321'],
    ] as const) {
      const spec = {data, mark, encoding: {...encoding, ...(color ? {color} : {})}}
      const compiled = compile(spec, {config: vegaTheme(theme)}).spec
      expect(JSON.stringify(compiled.marks)).toContain('"fill":{"value":"' + expected + '"}')
    }
  })
  it('reads locally resolved tokens and canonicalizes colors', () => {
    const host = document.createElement('div')
    document.body.append(host)
    const values: Record<string, string> = {'fg': '#edf1f7', 'muted': '#c1cad6', 'surface': '#131923', 'surface-2': '#1c2430',
      'border': '#405066', 'accent': 'rgb(229, 134, 92)', 'font': '"Example Sans", sans-serif', 'text-sm': '14px',
      'code-keyword': '#bc9cf0', 'code-string': '#8fc9a0', 'code-number': '#e0b36a', 'code-title': '#7fb2ee', 'code-type': '#6fc7c7'}
    for (const [key, value] of Object.entries(values)) host.style.setProperty('--chat-' + key, value)
    expect(readVisualTheme(host)).toEqual(theme)
  })
  it('observes inherited ancestor styles and system color-scheme changes', async () => {
    const ancestor = document.createElement('div')
    const host = document.createElement('div')
    ancestor.append(host); document.body.append(ancestor)
    let accent = '#dd7711'
    const computed = window.getComputedStyle.bind(window)
    vi.spyOn(window, 'getComputedStyle').mockImplementation((element, pseudo) => {
      const styles = computed(element, pseudo)
      // Model inherited / media-rule computed values without depending on jsdom's
      // partial custom-property inheritance and media-query implementation.
      return element === host ? new Proxy(styles, {get(target, key) {
        if (key === 'getPropertyValue') return (name: string) => name === '--chat-accent' ? accent : target.getPropertyValue(name)
        return Reflect.get(target, key)
      }}) : styles
    })
    const listeners = new Set<() => void>()
    const media = {addEventListener: vi.fn((_event: string, handler: () => void) => listeners.add(handler)),
      removeEventListener: vi.fn((_event: string, handler: () => void) => listeners.delete(handler))}
    vi.stubGlobal('matchMedia', () => media)
    const callback = vi.fn()
    const stop = observeVisualTheme(host, callback)
    accent = '#1188dd'
    ancestor.style.setProperty('--chat-accent', accent)
    await vi.waitFor(() => expect(callback).toHaveBeenCalledTimes(1))
    expect(callback.mock.calls[0][0].accent).toBe(accent)
    accent = '#bb99ee'
    for (const listener of listeners) listener()
    await vi.waitFor(() => expect(callback).toHaveBeenCalledTimes(2))
    expect(callback.mock.calls[1][0].accent).toBe(accent)
    stop()
    expect(media.removeEventListener).toHaveBeenCalledWith('change', expect.any(Function))
    expect(listeners.size).toBe(0)
  })
  it('coalesces ancestor/theme changes and notifies only actual token changes', async () => {
    const ancestor = document.createElement('div')
    const host = document.createElement('div')
    ancestor.append(host); document.body.append(ancestor)
    host.style.setProperty('--chat-accent', '#dd7711')
    const callback = vi.fn()
    const stop = observeVisualTheme(host, callback)
    ancestor.classList.add('unrelated')
    await new Promise(resolve => setTimeout(resolve, 40))
    expect(callback).not.toHaveBeenCalled()
    host.style.setProperty('--chat-accent', '#1188dd')
    host.style.setProperty('--chat-code-keyword', '#bb99ee')
    await vi.waitFor(() => expect(callback).toHaveBeenCalledTimes(1))
    expect(callback.mock.calls[0][0]).toMatchObject({accent: '#1188dd'})
    stop()
    host.style.setProperty('--chat-accent', '#ff0011')
    await new Promise(resolve => setTimeout(resolve, 40))
    expect(callback).toHaveBeenCalledTimes(1)
  })
})
