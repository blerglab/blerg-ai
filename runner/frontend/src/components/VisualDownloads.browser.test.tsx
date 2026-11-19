// BLG-451: Download SVG/PNG on a published chart/diagram's own tab, through the real renderers
// (no mocks) -- Vega-Lite and Mermaid, light and dark, at phone and 4K widths.
import { describe, expect, it, afterEach, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { render, waitFor, within } from '@testing-library/react'
import { ArtifactViewer, ChatContext, type ChatContextValue, type ArtifactInfo, type TransportFiles } from '@blerglab/chat'
import { exportScale } from '../../../../packages/chat/src/visual/vegaBudget'
import '@blerglab/chat/blerg.css'

const shots = import.meta.env.VITE_VISUAL_DOWNLOAD_SHOTS_DIR
// BLG-468: a plain flowchart like this now draws with Blerg's own flowchart renderer, not
// Mermaid's <img> — the same "poison pill" precedent BLERG_SOURCE's own strokeDash uses below
// for Vega. A cylinder shape keeps this on Mermaid's existing path, which this test is about.
const MERMAID_SOURCE = 'flowchart LR\n  A[Open chat] --> B[Pick a diagram]\n  B --> C{Published?}\n  C -->|yes| D[Follows new versions]\n  C -->|no| E[(Snapshot in a tab)]\n'
const VALUES = [{ month: 'Jan', count: 12 }, { month: 'Feb', count: 18 }, { month: 'Mar', count: 9 }, { month: 'Apr', count: 24 }]
// A plain bar chart now draws with Blerg's own renderer (BLG-452); a strokeDash poison pill keeps
// this source on Vega's canvas path, which the PNG-scale cases below measure.
const BLERG_SOURCE = JSON.stringify({ data: { values: VALUES }, mark: 'bar', encoding: { x: { field: 'month', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } } })
const VEGA_SOURCE = JSON.stringify({
  data: { values: VALUES },
  mark: { type: 'bar', strokeDash: [1, 0] },
  encoding: { x: { field: 'month', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } },
})

function files(source: string): TransportFiles {
  return { list: async () => [], raw: async () => new Blob([source], { type: 'text/plain' }), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') } }
}
function chat(f: TransportFiles): ChatContextValue {
  return { sessionId: 'visual-downloads', files: f, view: () => {}, latestVersion: () => undefined, byName: () => undefined, submitFeedback: async () => true, reviewFor: async () => null }
}
function artifact(name: string, source: string): ArtifactInfo {
  // visualFile() (model/visualKind.ts) only looks at .mmd/.mermaid names under view 'text';
  // a Vega-Lite spec needs view 'json' (the server assigns this from the file's bytes/name).
  const view = /\.(vl|vega-lite)\.json$/i.test(name) ? 'json' : 'text'
  return { id: name, version: 1, name, size: source.length, content_type: 'text/plain', view }
}
function setDark(dark: boolean) {
  document.documentElement.style.setProperty('--chat-fg', dark ? '#f0f3f7' : '#0f1115')
  document.documentElement.style.setProperty('--chat-surface', dark ? '#171b22' : '#ffffff')
  document.documentElement.style.setProperty('--chat-surface-2', dark ? '#1f242d' : '#eef1f5')
  document.documentElement.style.setProperty('--chat-border', dark ? '#313a48' : '#c7ccd3')
  document.documentElement.style.setProperty('--chat-accent', '#ff6a3d')
}
afterEach(() => {
  document.documentElement.removeAttribute('style')
  document.body.innerHTML = ''
})

async function open(name: string, source: string, width: number) {
  await page.viewport(width, width === 3840 ? 2160 : 844)
  const a = artifact(name, source)
  const f = files(source)
  const rendered = render(
    <ChatContext.Provider value={chat(f)}>
      <div style={{ height: '100vh', display: 'flex', minWidth: 0 }}>
        <ArtifactViewer embedded sessionId="visual-downloads" files={f} artifact={a} onClose={() => {}} />
      </div>
    </ChatContext.Provider>,
  )
  const viewer = await rendered.findByRole('region', { name: new RegExp(name) })
  return { ...rendered, viewer }
}

describe('chart/diagram tab: Download SVG/PNG through the real renderers', () => {
  for (const width of [390, 3840]) {
    it(`draws a published Vega-Lite chart and enables its downloads at ${width}px`, async () => {
      const { viewer } = await open('sprints.vl.json', VEGA_SOURCE, width)
      await waitFor(() => expect(viewer.querySelector('canvas')).not.toBeNull(), { timeout: 15000 })
      if (width === 390) await userEvent.click(within(viewer).getByRole('button', { name: 'More' }))
      const svgButton = within(viewer).getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement
      const pngButton = within(viewer).getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement
      await waitFor(() => expect(svgButton.disabled).toBe(false))
      expect(pngButton.disabled).toBe(false)
      expect(within(viewer).getByRole('button', { name: 'Download source' })).toBeInTheDocument()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width + 1)
      if (shots) await page.screenshot({ path: `${shots}/chart-tab-${width}.png` })
    }, 20000)

    for (const dark of [true, false]) {
      it(`draws a published Mermaid diagram themed ${dark ? 'dark' : 'light'} at ${width}px`, async () => {
        setDark(dark)
        const { viewer } = await open('flow.mmd', MERMAID_SOURCE, width)
        await waitFor(() => expect(viewer.querySelector('img')?.naturalWidth).toBeGreaterThan(0), { timeout: 15000 })
        if (width === 390) await userEvent.click(within(viewer).getByRole('button', { name: 'More' }))
        const svgButton = within(viewer).getByRole('button', { name: 'Download SVG' }) as HTMLButtonElement
        await waitFor(() => expect(svgButton.disabled).toBe(false))
        expect((within(viewer).getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement).disabled).toBe(false)
        expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width + 1)
        if (shots) await page.screenshot({ path: `${shots}/mermaid-tab-${dark ? 'dark' : 'light'}-${width}.png` })
      }, 20000)
    }
  }

  it('enables both downloads for a chart Blerg draws itself', async () => {
    const { viewer } = await open('sprints-blerg.vl.json', BLERG_SOURCE, 1280)
    await waitFor(() => expect(viewer.querySelector('.visual-surface svg, .visual-surface img')).not.toBeNull(), { timeout: 15000 })
    expect(viewer.querySelector('canvas')).toBeNull()
    for (const name of ['Download SVG', 'Download PNG']) {
      const button = within(viewer).getByRole('button', { name }) as HTMLButtonElement
      await waitFor(() => expect(button.disabled).toBe(false))
    }
  })

  it('exports a real PNG, at exportScale (not raw devicePixelRatio), filled with the theme surface', async () => {
    setDark(true)
    const { viewer } = await open('sprints.vl.json', VEGA_SOURCE, 1280)
    const onScreen = await waitFor(() => { const c = viewer.querySelector('canvas'); expect(c).not.toBeNull(); return c! }, { timeout: 15000 })
    const pngButton = within(viewer).getByRole('button', { name: 'Download PNG' }) as HTMLButtonElement
    await waitFor(() => expect(pngButton.disabled).toBe(false))

    // The display canvas's own CSS width is set to the drawing's exact logical width (vega.ts);
    // its pixel buffer is that width times the display path's own (floored, un-clamped-by-this-
    // card) scale -- recovering both gives exportScale's real input without reaching into
    // ArtifactViewer's internal state.
    const logicalWidth = Number(onScreen.style.width.replace('px', ''))
    const displayScale = onScreen.width / logicalWidth
    const logicalHeight = onScreen.height / displayScale

    let capturedCanvas: HTMLCanvasElement | null = null
    const realToBlob = HTMLCanvasElement.prototype.toBlob
    const toBlobSpy = vi.spyOn(HTMLCanvasElement.prototype, 'toBlob')
      .mockImplementation(function (this: HTMLCanvasElement, ...args: Parameters<typeof realToBlob>) {
        // eslint-disable-next-line @typescript-eslint/no-this-alias -- captured for the assertions below, after this call returns
        capturedCanvas = this
        return realToBlob.apply(this, args)
      })
    // Above exportScale's own cap of 2: proves the export canvas is sized from that clamp, not
    // from raw devicePixelRatio -- the real bug this guards is exportPng skipping the clamp.
    vi.stubGlobal('devicePixelRatio', 3)
    try {
      // A real browser has a real 2D context (unlike the chat package's jsdom unit tests): the
      // export must succeed silently, not fall back to the "Could not export" failure path.
      await userEvent.click(pngButton)
      expect(within(viewer).queryByRole('alert')).toBeNull()
      const scale = exportScale(logicalWidth, logicalHeight)
      expect(scale).toBeCloseTo(2) // clamped: a stubbed devicePixelRatio of 3 must not reach the canvas directly
      expect(capturedCanvas).not.toBeNull()
      expect(capturedCanvas!.width).toBeCloseTo(Math.max(1, Math.round(logicalWidth * scale)), -1)
      expect(capturedCanvas!.height).toBeCloseTo(Math.max(1, Math.round(logicalHeight * scale)), -1)
      // A corner, away from the chart's own marks: still the theme.surface fill underneath them,
      // proving exportPng filled the canvas before drawing -- not a transparent background.
      const [r, g, b] = capturedCanvas!.getContext('2d')!.getImageData(0, 0, 1, 1).data
      expect([r, g, b]).toEqual([0x17, 0x1b, 0x22]) // --chat-surface set by setDark(true)
    } finally {
      toBlobSpy.mockRestore()
      vi.unstubAllGlobals()
    }
  }, 20000)
})
