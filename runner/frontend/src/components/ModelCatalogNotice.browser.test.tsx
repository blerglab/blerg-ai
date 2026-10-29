import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import ModelCatalogNotice from './ModelCatalogNotice'
import RunColumn from './RunColumn'
import '../index.css'

it.each([390, 3840])('keeps launch and Session reconnect actions usable at %ipx', async width => {
  await page.viewport(width, 1000)
  const retry = vi.fn()
  const error = 'Cluster Codex needs a dedicated login to renew its access. For a person-owned launch, connect Codex under Settings → Managed connections and start a new cluster session. For an operator launch, configure an API key or use a person-owned launch.'
  render(<main style={{ width: '100%', padding: 16, boxSizing: 'border-box' }}>
    <section aria-label="Launch"><RunColumn runtime="cluster" onRuntimeChange={vi.fn()} kind="agent" onKindChange={vi.fn()}
      engine="codex" onEngineChange={vi.fn()} models={[]} model="" onModelChange={vi.fn()}
      modelsError={error} modelsReconnect onModelsRetry={retry} daemons={[]} selectedDaemon={null} onDaemonChange={vi.fn()}
      clusterConfigured availableEnginesHere={['codex']} clusterEngineBlocked={false} clusterGitMissing={false}
      settingsLink={<a href="/settings">Settings</a>} dangerouslySkipPermissions={false} onSkipPermissionsChange={vi.fn()} /></section>
    <section aria-label="Session"><ModelCatalogNotice state={{ models: [], loading: false, fallback: false, engineEfforts: [], error, reconnect: true, retry }} /></section>
  </main>)
  const links = screen.getAllByRole('link', { name: 'Reconnect in Settings' })
  expect(links).toHaveLength(2)
  for (const link of links) {
    link.focus()
    expect(link).toHaveFocus()
    expect(link).toHaveAttribute('href', expect.stringMatching(/\/settings#managed-connections$/))
    const rect = link.getBoundingClientRect()
    expect(rect.width).toBeGreaterThan(0)
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(width)
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
