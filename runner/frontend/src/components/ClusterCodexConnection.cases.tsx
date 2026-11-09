import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { clearLaunchCache } from '../lib/launchCache'
import LaunchSheet from './LaunchSheet'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
afterEach(() => { cleanup(); vi.unstubAllGlobals(); clearLaunchCache(); localStorage.clear() })

export function clusterCodexConnectionTests(viewport?: (width: number) => Promise<void>) {
  for (const width of viewport ? [390, 3840] : [390]) {
    it.each(['missing', 'reconnect', 'connected'])(`handles %s cluster Codex connection at ${width} px`, async state => {
      clearLaunchCache()
      localStorage.setItem('blerg.launch.engine', 'codex')
      vi.stubGlobal('fetch', vi.fn((url: string) => {
        if (url === '/api/repos') return json({ repos: [] })
        if (url === '/api/daemons') return json([])
        if (url === '/api/cluster/status') return json({ configured: true, available_engines: ['codex'] })
        if (url === '/api/me/credentials') return json({ engines: state === 'missing' ? [] : ['codex'], git: false })
        if (url.startsWith('/api/runner/launch-options?')) return json({ state: 'known', engines: state === 'missing' ? [] : [{ id: 'codex', name: 'Codex', restricted: true }] })
        if (url.startsWith('/api/models/codex')) return json(state === 'reconnect' ? { models: [], error: 'Dedicated login required', reconnect: true } : { models: [] })
        if (url === '/api/session-boards') return json({ configured: false, boards: [] })
        if (url === '/api/mcp/connections') return json({ connections: [] })
        return json({})
      }))
      if (viewport) await viewport(width)
      render(<MemoryRouter><LaunchSheet open onClose={() => {}} /></MemoryRouter>)
      await screen.findByTestId('launch-footer')
      const launch = screen.getByRole('button', { name: /^Launch/ })
      if (state !== 'connected') {
        const link = await screen.findByRole('link', { name: 'Connect Codex' })
        expect(link).toHaveAttribute('href', expect.stringMatching(/\/settings#managed-connections$/))
        expect(link).toHaveAttribute('target', '_blank')
        expect(screen.getByTestId('cluster-codex-connection')).toHaveTextContent('Connect Codex for cluster workspaces')
        expect(launch).toBeDisabled()
        if (viewport) {
          link.scrollIntoView({ block: 'center' })
          expect(link).toBeVisible()
          const rect = link.getBoundingClientRect()
          expect(rect.width).toBeGreaterThan(90)
          expect(rect.left).toBeGreaterThanOrEqual(0)
          expect(rect.right).toBeLessThanOrEqual(width)
          expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
        }
      } else {
        await waitFor(() => expect(launch).toBeEnabled())
        expect(screen.queryByRole('link', { name: 'Connect Codex' })).toBeNull()
      }
    })
  }
}
