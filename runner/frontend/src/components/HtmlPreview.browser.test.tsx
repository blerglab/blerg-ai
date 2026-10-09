import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { MediaPreview, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

// The inline preview of a published HTML page runs that page, unasked, as its card scrolls into
// view. Only a real browser enforces what keeps it harmless (jsdom ignores `sandbox` and CSP), so
// the page below tries what a hostile file would and reports back with postMessage, which a
// sandboxed frame may still do. The companion test for the viewer is ArtifactViewer.browser.test.
const PAGE = `<!doctype html><html><body style="margin:0"><div style="width:1024px;height:640px;background:#06c"></div><script>
const out = { ranScript: true, origin: location.origin, innerWidth: innerWidth }
const done = () => parent.postMessage({ previewProbe: out }, '*')
try { out.parentDocument = String(parent.document.title) } catch (e) { out.parentDocument = 'blocked:' + e.name }
try { out.localStorage = String(localStorage.length) } catch (e) { out.localStorage = 'blocked:' + e.name }
try { out.cookie = 'cookie:' + document.cookie } catch (e) { out.cookie = 'blocked:' + e.name }
try { out.popup = window.open('about:blank') ? 'opened' : 'blocked' } catch (e) { out.popup = 'blocked:' + e.name }
try { top.location.href = 'about:blank#hijacked'; out.topNavigation = 'attempted' } catch (e) { out.topNavigation = 'blocked:' + e.name }
fetch('/api/sessions').then(() => { out.fetch = 'reached' }, () => { out.fetch = 'blocked' }).then(done)
</script></body></html>`

const files: TransportFiles = {
  list: async () => [],
  raw: async () => new Blob([PAGE], { type: 'text/html' }),
  download: async () => {},
  remove: async () => {},
  upload: async () => { throw new Error('not in this test') },
}

describe('inline HTML preview (real browser)', () => {
  it('shows the page small, cut off from the app and the network, and only the cover can be clicked', async () => {
    const before = location.href
    const got = new Promise<Record<string, string | boolean | number>>(resolve => {
      window.addEventListener('message', e => {
        if (e.data && typeof e.data === 'object' && 'previewProbe' in e.data) resolve(e.data.previewProbe)
      })
    })
    const onOpen = vi.fn()
    const { container } = render(
      <div className="chat-root" style={{ width: 400 }}>
        <MediaPreview
          sessionId="s-html-preview"
          files={files}
          artifact={{ id: 'h1', name: 'probe.html', size: 900, content_type: 'text/html', view: 'html' }}
          onOpen={onOpen}
        />
      </div>,
    )
    const probe = await Promise.race([
      got,
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the page never reported')), 8000)),
    ])
    expect(probe.ranScript).toBe(true)
    expect(probe.origin).toBe('null') // an origin of its own, not the app's
    expect(String(probe.parentDocument)).toMatch(/^blocked:/)
    expect(String(probe.localStorage)).toMatch(/^blocked:/)
    expect(String(probe.cookie)).toMatch(/^blocked:/)
    expect(String(probe.popup)).toMatch(/^blocked/)
    expect(String(probe.topNavigation)).toMatch(/^blocked:/)
    expect(probe.fetch).toBe('blocked')
    expect(location.href).toBe(before) // the tab was not taken anywhere

    // Laid out as a desktop page, drawn at the card's width.
    expect(probe.innerWidth).toBe(1024)
    const box = container.querySelector('[data-testid="artifact-preview-html"]') as HTMLElement
    const frame = box.querySelector('iframe') as HTMLIFrameElement
    await waitFor(() => {
      const b = box.getBoundingClientRect()
      const f = frame.getBoundingClientRect()
      expect(b.width).toBeGreaterThan(200)
      expect(Math.abs(f.width - b.width)).toBeLessThan(4)
      expect(Math.abs(f.height - b.height)).toBeLessThan(4)
      expect(Math.abs(b.height / b.width - 640 / 1024)).toBeLessThan(0.02)
    })

    // What is under the pointer anywhere on the preview is the cover, never the page.
    const r = box.getBoundingClientRect()
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2) as HTMLElement
    expect(hit.closest('button')?.getAttribute('aria-label')).toBe('Open probe.html')
    hit.click()
    expect(onOpen).toHaveBeenCalledWith(false)
    // The page cannot take the keyboard: it is not a tab stop and holds no focus.
    expect(frame.tabIndex).toBe(-1)
    expect(document.activeElement).not.toBe(frame)
  }, 15_000)
})
