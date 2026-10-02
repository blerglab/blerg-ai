import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'

// A real browser is the only place the HTML artifact's sandbox can be checked: jsdom neither
// enforces `sandbox` nor CSP. The page below tries everything a hostile file would, from inside
// the viewer's iframe, and reports what happened back with postMessage (which a sandboxed frame
// may still do).
vi.mock('../apiFetch', () => ({
  apiFetch: () => Promise.resolve(new Response(PAGE)),
}))

const PAGE = `<!doctype html><html><body><script>
const out = {}
const done = () => parent.postMessage({ artifactProbe: out }, '*')
try { out.parentDocument = String(parent.document.title) } catch (e) { out.parentDocument = 'blocked:' + e.name }
try { out.localStorage = String(localStorage.length) } catch (e) { out.localStorage = 'blocked:' + e.name }
try { out.cookie = 'cookie:' + document.cookie } catch (e) { out.cookie = 'blocked:' + e.name }
out.origin = location.origin
out.ranScript = true
fetch('/api/sessions').then(() => { out.fetch = 'reached' }, () => { out.fetch = 'blocked' }).then(() => {
  const img = new Image()
  img.onload = () => { out.remoteImage = 'loaded'; done() }
  img.onerror = () => { out.remoteImage = 'blocked'; done() }
  img.src = location.href.replace(/^.*$/, 'http://127.0.0.1:1/x.png')
})
</script></body></html>`

afterEach(() => { vi.restoreAllMocks() })

describe('ArtifactViewer html sandbox (real browser)', () => {
  it('runs the page’s script but cuts it off from the app and the network', async () => {
    const got = new Promise<Record<string, string | boolean>>(resolve => {
      window.addEventListener('message', e => {
        if (e.data && typeof e.data === 'object' && 'artifactProbe' in e.data) resolve(e.data.artifactProbe)
      })
    })
    render(
      <ArtifactViewer
        sessionId="s1"
        artifact={{ id: 'a1', name: 'probe.html', size: 500, content_type: 'text/html', view: 'html' }}
        onClose={() => {}}
      />,
    )
    const probe = await Promise.race([
      got,
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the page never reported')), 8000)),
    ])
    expect(probe.ranScript).toBe(true) // scripts do run
    expect(probe.origin).toBe('null') // an opaque origin, not the app's
    expect(String(probe.parentDocument)).toMatch(/^blocked:/) // cannot reach the app's DOM
    expect(String(probe.localStorage)).toMatch(/^blocked:/) // no app storage
    expect(String(probe.cookie)).toMatch(/^blocked:/) // no app cookies
    expect(probe.fetch).toBe('blocked') // no network
    expect(probe.remoteImage).toBe('blocked') // not even an image
  }, 15_000)
})
