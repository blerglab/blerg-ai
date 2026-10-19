import type { CSSProperties } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { userEvent } from 'vitest/browser'
import { Markdown, Transcript, VisualBlock, type AgentEvent } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const diagrams = {
  flowchart: 'flowchart LR\n  Start --> Finish',
  sequence: 'sequenceDiagram\n  Alice->>Bob: Hello\n  Bob-->>Alice: Welcome',
  state: 'stateDiagram-v2\n  [*] --> Ready\n  Ready --> Finished',
  ER: 'erDiagram\n  OWNER ||--o{ SESSION : owns\n  OWNER {\n    string name\n  }',
  gantt: 'gantt\n  title Release\n  dateFormat YYYY-MM-DD\n  section Work\n  Build :2026-01-01, 2d',
}

const values = [
  { category: 'Alpha', x: 1, y: 2 },
  { category: 'Beta', x: 2, y: 4 },
  { category: 'Gamma', x: 3, y: 3 },
]

function chart(mark: string) {
  return JSON.stringify({
    $schema: 'https://vega.github.io/schema/vega-lite/v6.json',
    width: 300,
    height: 180,
    data: { values },
    mark,
    encoding: {
      x: { field: mark === 'bar' || mark === 'rect' ? 'category' : 'x', type: mark === 'bar' || mark === 'rect' ? 'nominal' : 'quantitative' },
      y: { field: 'y', type: mark === 'rect' ? 'ordinal' : 'quantitative' },
      ...(mark === 'rect' ? { color: { field: 'x', type: 'quantitative' } } : {}),
      tooltip: [{ field: 'category', type: 'nominal' }, { field: 'y', type: 'quantitative' }],
    },
  })
}

async function renderedGraphic(container: HTMLElement) {
  await waitFor(() => {
    expect(container.querySelector('img.visual-diagram, canvas, .md-card-note')).not.toBeNull()
  }, { timeout: 30_000 })
  const graphic = container.querySelector<HTMLImageElement | HTMLCanvasElement>('img.visual-diagram, canvas')
  expect(graphic, container.textContent ?? 'No rendered graphic').not.toBeNull()
  await waitFor(() => {
    expect(graphic!.getBoundingClientRect().width).toBeGreaterThan(20)
    expect(graphic!.getBoundingClientRect().height).toBeGreaterThan(20)
    if (graphic instanceof HTMLImageElement) expect(graphic.naturalWidth).toBeGreaterThan(20)
  })
  return graphic!
}

async function hoverChartData(canvas: HTMLCanvasElement) {
  const pixels = canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height)
  const bounds = canvas.getBoundingClientRect()
  // Locate colored data, not axes, then dispatch real Chromium pointer moves.
  for (let y = 0; y < canvas.height; y += 1) {
    for (let x = 0; x < canvas.width; x += 1) {
      const i = (y * canvas.width + x) * 4
      const [r, g, b, alpha] = pixels.data.slice(i, i + 4)
      if (alpha! < 10 || Math.max(r!, g!, b!) - Math.min(r!, g!, b!) < 20) continue
      await userEvent.hover(canvas, {position: {x: x * bounds.width / canvas.width, y: y * bounds.height / canvas.height}})
      const tooltip = document.querySelector<HTMLElement>('.visual-tooltip:not([hidden])')
      if (tooltip?.textContent) return tooltip
    }
  }
  throw new Error('Hovering painted chart data never produced its tooltip: ' + document.querySelector('.visual-tooltip')?.outerHTML)
}

const THEMES = [
  {name: 'dark', surface: '#161c26', surface2: '#1e2633', fg: '#f2f6fa', muted: '#c2ccd8', border: '#313d4f', accent: '#ff6a3d'},
  {name: 'light', surface: '#e5eaf1', surface2: '#dbe1e9', fg: '#172130', muted: '#43566d', border: '#b9c3d2', accent: '#a64000'},
] as const

function applyTheme(theme: typeof THEMES[number]) {
  for (const [token, value] of Object.entries(theme)) {
    if (token !== 'name' && token in THEMES[0]) document.documentElement.style.setProperty(`--chat-${token === 'surface2' ? 'surface-2' : token}`, value)
  }
}

function rgb(color: string): number[] {
  const canvas = document.createElement('canvas')
  canvas.width = canvas.height = 1
  const context = canvas.getContext('2d')!
  context.fillStyle = color
  context.fillRect(0, 0, 1, 1)
  return [...context.getImageData(0, 0, 1, 1).data].slice(0, 3)
}

function contrast(a: string, b: string): number {
  const luminance = (color: string) => rgb(color).map(component => {
    const v = component / 255
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }).reduce((sum, component, index) => sum + component * [0.2126, 0.7152, 0.0722][index]!, 0)
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (light! + 0.05) / (dark! + 0.05)
}

function diagramTextColors(image: HTMLImageElement): string[] {
  // Inspect the renderer's actual sanitized SVG styles in an isolated shadow tree.
  // The displayed diagram remains an inert image; the temporary tree is removed immediately.
  const source = decodeURIComponent(image.src.split(',')[1]!)
  const svg = new DOMParser().parseFromString(source, 'image/svg+xml').documentElement
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;left:-100000px;visibility:hidden'
  document.body.append(host)
  const shadow = host.attachShadow({mode: 'open'})
  shadow.append(document.importNode(svg, true))
  try {
    return [...shadow.querySelectorAll('text, tspan')].filter(element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent?.trim())).map(text => getComputedStyle(text).fill).filter(fill => fill !== 'none')
  } finally { host.remove() }
}

function transparentCorner(canvas: HTMLCanvasElement): boolean {
  const context = canvas.getContext('2d')!
  const scale = canvas.width / canvas.getBoundingClientRect().width
  return context.getImageData(Math.max(0, Math.floor(scale)), Math.max(0, Math.floor(scale)), 1, 1).data[3] === 0
}

function canvasHasColor(canvas: HTMLCanvasElement, color: string): boolean {
  const expected = rgb(color)
  const pixels = canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height).data
  for (let i = 0; i < pixels.length; i += 4) {
    if (pixels[i + 3] === 255 && expected.every((value, channel) => pixels[i + channel] === value)) return true
  }
  return false
}

afterEach(() => vi.restoreAllMocks())

describe('rich output in Chromium', () => {
  for (const [name, source] of Object.entries(diagrams)) {
    it(`renders a real Mermaid ${name} diagram`, async () => {
      const { container } = render(<VisualBlock kind="mermaid" source={source} />)
      const image = await renderedGraphic(container)
      expect(image).toBeInstanceOf(HTMLImageElement)
      const svg = decodeURIComponent((image as HTMLImageElement).src.split(',')[1]!)
      expect(svg).toMatch(/<(?:path|rect|line|polygon)/)
      expect(svg).not.toContain('Syntax error')
    }, 40_000)
  }

  it.each(THEMES.flatMap(theme => Object.entries(diagrams).map(([family, source]) => ({...theme, family, source}))))('renders readable native Mermaid $family in $name, inline and fullscreen', async theme => {
    const root = document.documentElement
    const previousStyle = root.getAttribute('style')
    applyTheme(theme)
    try {
      const {container, getByRole} = render(<div style={{background: theme.surface, color: theme.fg}}>
        <VisualBlock kind="mermaid" source={theme.source} />
      </div>)
      const assertNative = (image: HTMLImageElement) => {
        expect(getComputedStyle(image).backgroundColor).toBe('rgba(0, 0, 0, 0)')
        const colors = diagramTextColors(image)
        expect(colors.length).toBeGreaterThan(0)
        for (const color of colors) {
          expect(contrast(color, theme.surface), `text ${color} on ${theme.name} surface`).toBeGreaterThanOrEqual(4.5)
          expect(contrast(color, theme.surface2), `text ${color} on ${theme.name} node`).toBeGreaterThanOrEqual(4.5)
        }
      }
      assertNative(await renderedGraphic(container) as HTMLImageElement)
      await userEvent.click(getByRole('button', {name: 'Open'}))
      const dialog = getByRole('dialog')
      expect(rgb(getComputedStyle(dialog).backgroundColor)).toEqual(rgb(theme.surface))
      assertNative(await renderedGraphic(dialog) as HTMLImageElement)
      await userEvent.click(getByRole('button', {name: 'Close'}))
    } finally {
      if (previousStyle === null) root.removeAttribute('style')
      else root.setAttribute('style', previousStyle)
    }
  }, 40_000)

  it('preserves a locally scoped chat theme when opening a diagram portal', async () => {
    const root = document.documentElement
    const previousStyle = root.getAttribute('style')
    applyTheme(THEMES[0])
    const scoped = THEMES[1]
    const style = Object.fromEntries(Object.entries(scoped).filter(([key]) => key !== 'name').map(([key, value]) => [`--chat-${key === 'surface2' ? 'surface-2' : key}`, value])) as CSSProperties
    try {
      const {container, getByRole} = render(<div className="agent-chat" style={style}><VisualBlock kind="mermaid" source={diagrams.sequence} /></div>)
      const inline = await renderedGraphic(container) as HTMLImageElement
      for (const color of diagramTextColors(inline)) expect(contrast(color, scoped.surface)).toBeGreaterThanOrEqual(4.5)
      await userEvent.click(getByRole('button', {name: 'Open'}))
      const dialog = getByRole('dialog')
      expect(rgb(getComputedStyle(dialog).backgroundColor)).toEqual(rgb(scoped.surface))
      const image = await renderedGraphic(dialog) as HTMLImageElement
      expect(getComputedStyle(image).backgroundColor).toBe('rgba(0, 0, 0, 0)')
      for (const color of diagramTextColors(image)) expect(contrast(color, scoped.surface)).toBeGreaterThanOrEqual(4.5)
      await userEvent.click(getByRole('button', {name: 'Close'}))
    } finally {
      if (previousStyle === null) root.removeAttribute('style')
      else root.setAttribute('style', previousStyle)
    }
  }, 40_000)

  it('keeps ordinary Mermaid labels near chat text size instead of the SVG 300px fallback', async () => {
    const source = 'flowchart LR\n Request --> Review\n Review --> Approved\n Approved --> Delivered'
    const event: AgentEvent = {type: 'agent_event', session_id: 'native-font-size', client_event_id: 'diagram-answer', kind: 'assistant_text', ts: '2026-10-18T12:00:00Z', payload: {text: '```mermaid\n' + source + '\n```'}}
    const {container} = render(<div className="agent-chat" style={{width: 800, height: 500}}><Transcript sessionId="native-font-size" events={[event]} streaming="" compact={false} liveIds={new Set()} showSkeleton={false} loadingOlder={false} starting={false} attempt={null} clockOffset={0} readyCard={null} working={false} waiting={false} since={null} waitingOn={null} queued={[]} queueState="queued" steers pin={0} /></div>)
    const image = await renderedGraphic(container) as HTMLImageElement
    const svg = new DOMParser().parseFromString(decodeURIComponent(image.src.split(',')[1]!), 'image/svg+xml').documentElement
    const viewBoxWidth = Number(svg.getAttribute('viewBox')?.split(/[ ,]+/)[2])
    expect(viewBoxWidth).toBeGreaterThan(0)
    const declaredFont = /font-size:\s*([\d.]+)px/.exec(svg.querySelector('style')?.textContent ?? '')
    expect(declaredFont).not.toBeNull()
    const displayedFont = Number(declaredFont![1]) * image.getBoundingClientRect().width / viewBoxWidth
    expect(displayedFont).toBeGreaterThanOrEqual(11)
  }, 40_000)

  it('keeps visual controls quiet until the message is hovered or keyboard focused', async () => {
    const {container, getByRole} = render(<><button type="button">Outside the message</button><VisualBlock kind="mermaid" source={diagrams.flowchart} /></>)
    await renderedGraphic(container)
    const open = getByRole('button', {name: 'Open'})
    const outside = getByRole('button', {name: 'Outside the message'})
    await userEvent.hover(outside)
    await waitFor(() => expect(getComputedStyle(open).opacity).toBe('0'))
    await userEvent.hover(container.querySelector('.visual-block')!)
    await waitFor(() => expect(getComputedStyle(open).opacity).toBe('1'))
    await userEvent.hover(outside)
    open.focus()
    await waitFor(() => expect(getComputedStyle(open).opacity).toBe('1'))
    await userEvent.keyboard('{Enter}')
    expect(getByRole('dialog')).not.toBeNull()
    await userEvent.click(getByRole('button', {name: 'Close'}))
  }, 40_000)

  it.each(THEMES.flatMap(theme => ['bar', 'line'].map(mark => ({...theme, mark}))))('paints native Vega $mark labels with AA contrast and no white canvas in $name', async theme => {
    const root = document.documentElement
    const previousStyle = root.getAttribute('style')
    applyTheme(theme)
    const painted: Array<{text: string; color: string}> = []
    const original = CanvasRenderingContext2D.prototype.fillText
    vi.spyOn(CanvasRenderingContext2D.prototype, 'fillText').mockImplementation(function (this: CanvasRenderingContext2D, text, x, y, maxWidth) {
      if (text.trim()) painted.push({text, color: String(this.fillStyle)})
      if (maxWidth === undefined) original.call(this, text, x, y)
      else original.call(this, text, x, y, maxWidth)
    })
    try {
      const {container, getByRole} = render(<div style={{background: theme.surface}}><VisualBlock kind="vega-lite" source={chart(theme.mark)} /></div>)
      const inline = await renderedGraphic(container) as HTMLCanvasElement
      expect(transparentCorner(inline)).toBe(true)
      expect(painted.length).toBeGreaterThan(0)
      for (const label of painted) expect(contrast(label.color, theme.surface), `${label.text}: ${label.color}`).toBeGreaterThanOrEqual(4.5)
      painted.length = 0
      await userEvent.click(getByRole('button', {name: 'Open'}))
      const fullscreen = await renderedGraphic(getByRole('dialog')) as HTMLCanvasElement
      expect(transparentCorner(fullscreen)).toBe(true)
      expect(painted.length).toBeGreaterThan(0)
      for (const label of painted) expect(contrast(label.color, theme.surface)).toBeGreaterThanOrEqual(4.5)
      await userEvent.click(getByRole('button', {name: 'Close'}))
    } finally {
      if (previousStyle === null) root.removeAttribute('style')
      else root.setAttribute('style', previousStyle)
    }
  }, 40_000)

  it('redraws mounted diagrams and charts when theme tokens change', async () => {
    const root = document.documentElement
    const previousStyle = root.getAttribute('style')
    applyTheme(THEMES[0])
    try {
      const categorical = JSON.parse(chart('bar'))
      categorical.encoding.color = {field: 'category', type: 'nominal'}
      const {container} = render(<div><VisualBlock kind="mermaid" source={diagrams.flowchart} /><VisualBlock kind="vega-lite" source={JSON.stringify(categorical)} /></div>)
      const image = await waitFor(() => {const node = container.querySelector<HTMLImageElement>('img.visual-diagram'); expect(node?.naturalWidth).toBeGreaterThan(20); return node!})
      const canvas = await waitFor(() => {const node = container.querySelector('canvas'); expect(node).not.toBeNull(); return node!})
      const beforeImage = image.src
      const beforeChart = canvas.toDataURL()
      applyTheme(THEMES[1])
      await waitFor(() => {const next = container.querySelector<HTMLImageElement>('img.visual-diagram'); expect(next).not.toBeNull(); expect(next!.naturalWidth).toBeGreaterThan(20); expect(next!.src).not.toBe(beforeImage)}, {timeout: 15_000})
      await waitFor(() => {const next = container.querySelector('canvas'); expect(next).not.toBeNull(); expect(next!.toDataURL()).not.toBe(beforeChart); expect(canvasHasColor(next!, THEMES[1].accent)).toBe(true)}, {timeout: 15_000})
      const afterImage = container.querySelector<HTMLImageElement>('img.visual-diagram')!
      for (const color of diagramTextColors(afterImage)) expect(contrast(color, THEMES[1].surface)).toBeGreaterThanOrEqual(4.5)
      const lightChart = container.querySelector('canvas')!.toDataURL()
      // Code-color token changes also trigger a redraw without remounting the visual.
      root.style.setProperty('--chat-code-keyword', '#087830')
      await waitFor(() => {const next = container.querySelector('canvas'); expect(next).not.toBeNull(); expect(next!.toDataURL()).not.toBe(lightChart); expect(canvasHasColor(next!, '#087830')).toBe(true)}, {timeout: 15_000})
    } finally {
      if (previousStyle === null) root.removeAttribute('style')
      else root.setAttribute('style', previousStyle)
    }
  }, 40_000)

  it('fits ordinary diagrams and default charts within a phone message', async () => {
    const spec = JSON.parse(chart('bar'))
    delete spec.width
    delete spec.height
    const {container} = render(<div style={{width: 340}}><VisualBlock kind="mermaid" source={diagrams.flowchart} /><VisualBlock kind="vega-lite" source={JSON.stringify(spec)} /></div>)
    await waitFor(() => expect(container.querySelector('img.visual-diagram')).not.toBeNull(), {timeout: 30_000})
    await waitFor(() => expect(container.querySelector('canvas')).not.toBeNull(), {timeout: 30_000})
    for (const surface of container.querySelectorAll<HTMLElement>('.visual-surface')) {
      expect(surface.scrollWidth).toBeLessThanOrEqual(surface.clientWidth + 1)
      const graphic = surface.querySelector<HTMLElement>('img, canvas')!
      expect(graphic.getBoundingClientRect().width).toBeLessThanOrEqual(340)
    }
  }, 40_000)

  for (const [name, mark] of [['bar', 'bar'], ['line', 'line'], ['scatter', 'point'], ['heatmap', 'rect']]) {
    it(`renders a real Vega-Lite ${name} chart with pointer tooltips`, async () => {
      const { container } = render(<VisualBlock kind="vega-lite" source={chart(mark!)} />)
      const canvas = await renderedGraphic(container)
      expect(canvas).toBeInstanceOf(HTMLCanvasElement)
      const tooltip = await hoverChartData(canvas as HTMLCanvasElement)
      expect(tooltip.textContent).toMatch(/Alpha|Beta|Gamma/)
      expect(getComputedStyle(tooltip).visibility).not.toBe('hidden')
    }, 40_000)
  }

  it('keeps the original source available and opens an enlarged, zoomable view', async () => {
    const source = diagrams.flowchart
    const { container, getByRole } = render(<VisualBlock kind="mermaid" source={source} />)
    await renderedGraphic(container)
    await userEvent.click(getByRole('button', { name: /^source$/i }))
    expect(container.textContent).toContain(source)
    const openButton = getByRole('button', { name: /^open$/i })
    await userEvent.click(openButton)
    const dialog = getByRole('dialog')
    expect(dialog.getBoundingClientRect().width).toBeGreaterThan(300)
    await userEvent.tab()
    expect(document.activeElement).toBe(getByRole('button', { name: /zoom out/i }))
    await userEvent.tab({shift: true})
    expect(document.activeElement).toBe(getByRole('button', { name: /close/i }))
    await userEvent.tab()
    expect(document.activeElement).toBe(getByRole('button', { name: /zoom out/i }))
    const surface = dialog.querySelector<HTMLElement>('.visual-surface')!
    expect(surface.style.zoom).toBe('1')
    await userEvent.click(getByRole('button', { name: /zoom in/i }))
    expect(surface.style.zoom).toBe('1.25')
    await userEvent.click(getByRole('button', { name: /zoom out/i }))
    expect(surface.style.zoom).toBe('1')
    await userEvent.click(getByRole('button', { name: /zoom in/i }))
    await userEvent.click(getByRole('button', { name: /reset zoom/i }))
    expect(surface.style.zoom).toBe('1')
    await userEvent.click(getByRole('button', { name: /close/i }))
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(document.activeElement).toBe(openButton)
  }, 40_000)

  it.each([
    ['mermaid', 'this is definitely not a diagram'],
    ['vega-lite', '{not valid JSON'],
  ] as const)('preserves invalid %s source in its fallback', async (kind, source) => {
    const { container } = render(<VisualBlock kind={kind} source={source} />)
    await waitFor(() => expect(container.textContent).toContain(source), { timeout: 30_000 })
    expect(container.querySelector('svg')).toBeNull()
  }, 40_000)

  it('leaves ordinary fences unchanged while rendering visual fences', async () => {
    const { container } = render(<Markdown text={'```mermaid\n' + diagrams.flowchart + '\n```\n\n```typescript\nconst answer = 42\n```'} />)
    await renderedGraphic(container)
    expect(container.querySelector('pre[data-lang="typescript"] code')?.textContent).toContain('const answer = 42')
  }, 40_000)

  it('shows an HTML-looking tooltip value as inert text', async () => {
    const label = '<img src="https://attacker.invalid/pixel" onerror="alert(1)">'
    const spec = JSON.parse(chart('bar'))
    spec.data.values = [{category: label, x: 1, y: 2}]
    spec.encoding.x = {field: 'x', type: 'quantitative'}
    const {container} = render(<VisualBlock kind="vega-lite" source={JSON.stringify(spec)} />)
    const canvas = await renderedGraphic(container)
    const tooltip = await hoverChartData(canvas as HTMLCanvasElement)
    expect(tooltip.textContent).toContain(label)
    expect(tooltip.querySelector('img, script, iframe, a')).toBeNull()
    expect(performance.getEntriesByType('resource').some(entry => entry.name.includes('attacker.invalid'))).toBe(false)
  }, 40_000)

  it('refuses hostile render inputs before they can fetch data or install active SVG', async () => {
    const requests: string[] = []
    const realFetch = window.fetch.bind(window)
    vi.spyOn(window, 'fetch').mockImplementation((input, init) => {
      const url = String(input instanceof Request ? input.url : input)
      if (url.includes('attacker.invalid')) {
        requests.push(url)
        return Promise.reject(new Error('outbound probe blocked by test'))
      }
      return realFetch(input, init)
    })
    vi.spyOn(XMLHttpRequest.prototype, 'open').mockImplementation(function (_method: string, url: string | URL) {
      if (String(url).includes('attacker.invalid')) requests.push(String(url))
      throw new Error('unexpected XHR during rendering')
    })
    const attacks = [
      { kind: 'mermaid' as const, source: 'flowchart LR\n A[Click] --> B[End]\n click A "https://attacker.invalid/steal"' },
      { kind: 'mermaid' as const, source: '%%{init: {"securityLevel":"loose"}}%%\nflowchart LR\n A["<img src=https://attacker.invalid/image onerror=alert(1)>"]' },
      { kind: 'vega-lite' as const, source: JSON.stringify({ data: { url: 'https://attacker.invalid/data' }, mark: 'bar' }) },
      { kind: 'vega-lite' as const, source: JSON.stringify({ data: { values: [{ url: 'https://attacker.invalid/image' }] }, mark: 'image', encoding: { url: { field: 'url' } } }) },
      { kind: 'vega-lite' as const, source: JSON.stringify({ data: { values }, mark: { type: 'point', href: 'https://attacker.invalid/link' } }) },
    ]
    const { container } = render(<>{attacks.map((attack, i) => <VisualBlock key={i} {...attack} />)}</>)
    await waitFor(() => {
      for (const attack of attacks) expect(container.textContent).toContain(attack.source)
    }, { timeout: 30_000 })
    expect(requests).toEqual([])
    expect(container.querySelector('script, iframe, object, embed, svg a, svg image, svg foreignObject')).toBeNull()
    const resources = performance.getEntriesByType('resource').map(e => e.name)
    expect(resources.filter(url => url.includes('attacker.invalid'))).toEqual([])
  }, 40_000)
})
