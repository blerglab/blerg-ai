import { describe, expect, it } from 'vitest'
import { renderMermaid } from '../../../../../packages/chat/src/visual/mermaid'
import { renderInFrame } from '../../../../../packages/chat/src/visual/mermaidFrame'
import { readVisualTheme } from '../../../../../packages/chat/src/visual/theme'
import cases from './cases.json'

// Four isolated pages share one Chromium. Each page uses the production queue and opaque frame;
// no concurrent React cleanup can remove another case's host. npm test discovers all four shards.
export function corpus(shard: number) {
  describe('Mermaid 12.2.0 corpus', () => {
    it.each(cases.filter((_, i) => i % 4 === shard))('$id [$diagram]', async fixture => {
      const host = document.createElement('section')
      document.body.append(host)
      let dispose: (() => void) | undefined
      try {
        if (fixture.refusal || fixture.error || fixture.upstream) {
          await expect(renderMermaid(fixture.source, host), fixture.id).rejects.toThrow(fixture.refusal || fixture.error || fixture.upstream!.error)
          expect(host.children, `${fixture.id}: failed output must remain empty`).toHaveLength(0)
          if (fixture.upstream) {
            // Prove this limitation belongs to the installed bundle, through its real iframe,
            // with Blerg's validator and sanitizer bypassed. An upstream fix must fail this test.
            await expect(renderInFrame(fixture.source, { startOnLoad: false, securityLevel: 'strict', suppressErrorRendering: true, htmlLabels: false }, false), fixture.upstream.reason).rejects.toThrow(fixture.upstream.error)
          }
          return
        }
        if (fixture.theme) {
          const dark = fixture.theme === 'dark'
          host.style.setProperty('--chat-fg', dark ? '#eeeeee' : '#111111')
          host.style.setProperty('--chat-surface-2', dark ? '#222222' : '#ffffff')
          host.style.setProperty('--chat-surface', dark ? '#171717' : '#f5f5f5')
        }
        dispose = await renderMermaid(fixture.source, host, readVisualTheme(host))
        const image = host.querySelector('img')!
        expect(image, `${fixture.id}: renderer produced no image`).not.toBeNull()
        expect(image.naturalWidth, `${fixture.id}: empty image`).toBeGreaterThan(0)
        expect(image.naturalHeight, `${fixture.id}: empty image`).toBeGreaterThan(0)
        const raw = decodeURIComponent(image.src.split(',')[1])
        const svg = new DOMParser().parseFromString(raw, 'image/svg+xml')
        expect(svg.querySelector('parsererror, .error-icon, .error-text'), `${fixture.id}: error SVG`).toBeNull()
        const bounds = svg.documentElement.getAttribute('viewBox')?.split(/[\s,]+/).map(Number)
          ?? [0, 0, image.naturalWidth, image.naturalHeight]
        expect(bounds, `${fixture.id}: missing bounds`).toHaveLength(4)
        expect(bounds!.every(Number.isFinite), `${fixture.id}: nonfinite bounds`).toBe(true)
        expect(bounds![2], `${fixture.id}: zero width`).toBeGreaterThan(0)
        expect(bounds![3], `${fixture.id}: zero height`).toBeGreaterThan(0)
        const labels = [...svg.querySelectorAll('text')].map(t => {
          const walker = svg.createTreeWalker(t, NodeFilter.SHOW_TEXT)
          const words: string[] = []
          while (walker.nextNode()) words.push(walker.currentNode.textContent ?? '')
          const label = words.join(' ').replace(/\s+/g, ' ').trim()
          // Mermaid invents a random hash when a git commit has no explicit id.
          return fixture.diagram === 'gitGraph' ? label.replace(/^(\d+)-[a-f0-9]{7}$/, '$1-<generated>') : label
        })
        for (const label of fixture.labels) expect(labels.join('').replace(/\s/g, ''), `${fixture.id}: missing label ${label}`).toContain(label.replace(/\s/g, ''))
        if (fixture.exactLabels) {
          expect(labels, `${fixture.id}: complete literal text`).toEqual(fixture.exactLabels)
          expect(svg.querySelector('foreignObject, script, b, a, image, use, animate'), `${fixture.id}: unexpected markup`).toBeNull()
          for (const el of svg.querySelectorAll('*')) {
            expect(el.namespaceURI, fixture.id).toBe('http://www.w3.org/2000/svg')
            for (const attr of el.attributes) expect(attr.name, fixture.id).not.toMatch(/^on|href$/i)
          }
        }
        for (const color of fixture.colors ?? []) expect(raw.toLowerCase(), `${fixture.id}: missing color ${color}`).toContain(color)
        if (fixture.upstreamMissing) {
          const original = await renderInFrame(fixture.source, { startOnLoad: false, securityLevel: 'strict', htmlLabels: false }, false)
          expect(original, fixture.upstreamMissing.reason).not.toContain(fixture.upstreamMissing.label)
        }
        if (fixture.theme) expect(raw).toContain(fixture.theme === 'dark' ? '#eeeeee' : '#111111')
        const tags: Record<string, number> = {}
        const classes: Record<string, number> = {}
        for (const el of svg.querySelectorAll('*')) {
          tags[el.localName] = (tags[el.localName] ?? 0) + 1
          for (const token of el.getAttribute('class')?.split(/\s+/).filter(Boolean) ?? []) {
            const semantic = fixture.diagram === 'gitGraph' ? token.replace(/^(\d+)-[a-f0-9]{7}$/, '$1-<generated>') : token
            const name = semantic.replace(/^width-\d+(?:\.\d+)?$/, 'width-<measured>')
            classes[name] = (classes[name] ?? 0) + 1
          }
        }
        // Structural baselines ignore generated IDs and layout coordinates.
        // They retain SVG element/class counts and ordered visible labels: an empty/partial SVG,
        // a lost node/edge, or missing text cannot pass just because an SVG exists.
        expect({ tags, classes, labels }).toMatchSnapshot()
        const frame = document.querySelector<HTMLIFrameElement>('iframe[data-blerg-mermaid-frame]')!
        expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
        expect(frame.srcdoc).toContain("default-src 'none'")
      } finally {
        dispose?.()
        host.remove()
      }
    }, 30_000)
  })
}
