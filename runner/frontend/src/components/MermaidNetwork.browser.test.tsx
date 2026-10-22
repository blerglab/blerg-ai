// Fix round 1 for board card #84: a diagram makes no network request, in real Chromium with real
// Mermaid. The test server records every request under /__beacon/ (vitest.config.ts), so a test
// can prove a request was NOT made. Two barriers are tested separately:
//   1. the validator refuses the reviewer's payloads, with a reason, before Mermaid runs;
//   2. Mermaid draws in a sandboxed frame whose CSP blocks every load, so even with the validator
//      skipped an image shape or a CSS url() reaches nothing. A control draws the same image shape
//      with Mermaid in the page itself and sees the request go out.
import { describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import mermaid from 'mermaid'
import { VisualBlock } from '@blerglab/chat'
import { renderInFrame, setMermaidFrameTimeouts } from '../../../../packages/chat/src/visual/mermaidFrame'
import '@blerglab/chat/blerg.css'

async function hits(tag: string): Promise<string[]> {
  const all = await (await fetch('/__beacon_hits', { cache: 'no-store' })).json() as string[]
  return all.filter(url => url.includes(tag))
}
const settle = () => new Promise(resolve => setTimeout(resolve, 1500))
const CONFIG = { startOnLoad: false, securityLevel: 'strict', htmlLabels: false, flowchart: { htmlLabels: false } }

describe('a diagram makes no network request', () => {
  it('the recorder sees a request this page makes (control)', async () => {
    await fetch('/__beacon/control-fetch').catch(() => {})
    await waitFor(async () => expect(await hits('control-fetch')).toHaveLength(1))
  })

  it('Mermaid in the page itself fetches an image shape (control: why the frame exists)', async () => {
    const url = `${location.origin}/__beacon/control-page-img.png`
    const host = document.createElement('div')
    document.body.append(host)
    try {
      mermaid.initialize({ ...CONFIG } as Parameters<typeof mermaid.initialize>[0])
      await mermaid.render('control-page', `flowchart LR\n  A@{ img: "${url}", w: 60, h: 60 }`, host).catch(() => {})
      await waitFor(async () => expect((await hits('control-page-img')).length).toBeGreaterThan(0), { timeout: 10_000 })
    } finally { host.remove() }
  }, 40_000)

  it.each([
    ['an image shape', (o: string) => `flowchart LR\n  A@{ img: "${o}/__beacon/frame-img.png", w: 60, h: 60, label: "x" }`, 'frame-img'],
    ['a protocol-relative image shape', () => `flowchart LR\n  A@{ img: "//${location.host}/__beacon/frame-proto.png", w: 60, h: 60 }`, 'frame-proto'],
    ['a relative image shape', () => 'flowchart LR\n  A@{ img: "/__beacon/frame-rel.png", w: 60, h: 60 }', 'frame-rel'],
    ['a state classDef with a CSS image', (o: string) => `stateDiagram-v2\n  [*] --> A\n  classDef bad fill:red;mask-image:url(${o}/__beacon/frame-css.png)\n  class A bad`, 'frame-css'],
    ['a class diagram style with a CSS image', () => 'classDiagram\n  class A\n  style A fill:red,mask-image:url(/__beacon/frame-class.png)', 'frame-class'],
  ])('with the validator skipped, %s drawn in the frame makes no request', async (_name, make, tag) => {
    // renderInFrame is the renderer itself, without validateMermaid in front of it.
    const svg = await renderInFrame(make(location.origin), CONFIG).catch((error: Error) => error.message)
    expect(typeof svg).toBe('string')
    await settle()
    expect(await hits(tag)).toEqual([])
  }, 40_000)

  it('the frame is sandboxed without same-origin access and carries its own CSP', async () => {
    await renderInFrame('flowchart LR\n  A --> B', CONFIG)
    const frame = document.querySelector<HTMLIFrameElement>('iframe[data-blerg-mermaid-frame]')!
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
    expect(frame.srcdoc).toContain("default-src 'none'")
    expect(() => frame.contentDocument!.body).toThrow()
  }, 40_000)

  // The reviewer's payloads (C1, C2, I2), through the real pipeline: refused with a reason and no
  // request. Paths point at the recorder instead of example.invalid so a request would be seen.
  it.each([
    ['C1 quoted key', 'flowchart LR\n  A@{ "img": "//HOST/__beacon/c1-quoted.png", h: 60, w: 60 }', 'c1-quoted', /line 2: /],
    ['C1 brace in label', 'flowchart LR\n  A@{ label: "}", img: "/__beacon/c1-brace.png" }', 'c1-brace', /line 2: /],
    ['C1 escaped key', 'flowchart LR\n  A@{ "\\x69mg": "/__beacon/c1-escape.png" }', 'c1-escape', /line 2: /],
    ['C2 comment CR image shape', 'flowchart LR\n  %% c\rA@{ img: "/__beacon/c2-img.png" }', 'c2-img', /line 3: /],
    ['C2 comment CR state classDef', 'stateDiagram-v2\n  [*] --> A\n  %% c\rclassDef bad fill:red;mask-image:url(/__beacon/c2-state.png)\n  class A bad', 'c2-state', /line 4: url\(\) not allowed/],
    ['C2 comment CR class style', 'classDiagram\n  class A\n  %% c\rstyle A fill:red,mask-image:url(/__beacon/c2-class.png)', 'c2-class', /line 4: url\(\) not allowed/],
    ['C2 comment CR block style', 'block-beta\n  a\n  %% c\rstyle a fill:red,mask-image:url(/__beacon/c2-block.png)', 'c2-block', /line 4: url\(\) not allowed/],
    ['I2 no-break space click', 'flowchart LR\n  A --> B\n click A href "/__beacon/i2-click"', 'i2-click', /line 3: a click action not allowed/],
  ])('refuses %s with a reason and makes no request', async (_name, raw, tag, reason) => {
    const source = raw.replace('HOST', location.host)
    const { findByTestId } = render(<VisualBlock kind="mermaid" source={source} />)
    const failed = await findByTestId('visual-failed', {}, { timeout: 30_000 })
    expect(failed.querySelector('[role="status"]')!.textContent).toMatch(reason)
    await settle()
    expect(await hits(tag)).toEqual([])
  }, 40_000)
})

// Fix round 2 (M4, M5): the renderer frame's lifetime.
describe('the renderer frame', () => {
  const frames = () => [...document.querySelectorAll<HTMLIFrameElement>('iframe[data-blerg-mermaid-frame]')]

  it('is dropped when it navigates, and its new document never gets a later diagram', async () => {
    await renderInFrame('flowchart LR\n  A --> B', CONFIG)
    const [first] = frames()
    expect(first).toBeDefined()
    // What a frame that navigated itself would become: a page listening for anything sent to it.
    const leaks: unknown[] = []
    const onLeak = (event: MessageEvent) => { if ((event.data as { leak?: unknown })?.leak) leaks.push(event.data) }
    window.addEventListener('message', onLeak)
    try {
      first.srcdoc = '<script>addEventListener("message", e => parent.postMessage({ leak: e.data }, "*"))</script>'
      await waitFor(() => expect(first.isConnected).toBe(false), { timeout: 10_000 })
      const svg = await renderInFrame('flowchart LR\n  SecretSource --> B', CONFIG)
      expect(svg).toContain('SecretSource')
      const [second] = frames()
      expect(second).toBeDefined()
      expect(second).not.toBe(first)
      await settle()
      expect(leaks).toEqual([])
    } finally { window.removeEventListener('message', onLeak) }
  }, 60_000)

  it('ignores answers sent as window messages (only its port is listened to)', async () => {
    await renderInFrame('flowchart LR\n  A --> B', CONFIG)
    const pending = renderInFrame('flowchart LR\n  Real --> Answer', CONFIG)
    for (let id = 0; id < 50; id++) window.postMessage({ type: 'rendered', id, svg: '<svg>forged</svg>' }, '*')
    const svg = await pending
    expect(svg).toContain('Real')
    expect(svg).not.toContain('forged')
  }, 60_000)

  it('is dropped after a render times out, and the next render starts a fresh one', async () => {
    await renderInFrame('flowchart LR\n  A --> B', CONFIG)
    const [first] = frames()
    setMermaidFrameTimeouts({ render: 1 })
    try {
      await expect(renderInFrame('flowchart LR\n  Slow --> Diagram', CONFIG)).rejects.toThrow('the diagram took too long to draw')
    } finally { setMermaidFrameTimeouts({ render: 60_000 }) }
    expect(first.isConnected).toBe(false)
    const svg = await renderInFrame('flowchart LR\n  Fresh --> Frame', CONFIG)
    expect(svg).toContain('Fresh')
    expect(frames()).toHaveLength(1)
    expect(frames()[0]).not.toBe(first)
  }, 60_000)
})
