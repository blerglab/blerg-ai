import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import StartProgress from '../../../../packages/chat/src/components/StartProgress'
import type { StartAttempt } from '../../../../packages/chat/src/model/startStages'
import '../index.css'
import '@blerglab/chat/blerg.css'

it.each([390, 3840])('shows protected cluster launch failures and details at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const labels = [
    ['selection', 'Authorizing selection'], ['manifest', 'Saving workspace manifest'],
    ['plugins_resolve', 'Resolving launch plugins'], ['authorization', 'Checking launch authority'], ['credentials', 'Checking repository access'],
    ['storage', 'Allocating protected storage'], ['writer_fence', 'Fencing previous writers'],
    ['runtime_binding', 'Binding workspace generation'], ['launch_admission', 'Checking launch capacity'], ['runtime_token', 'Issuing runtime authority'],
    ['launch_credentials', 'Preparing engine credentials'], ['pod_manifest', 'Validating pod bootstrap'],
    ['secrets', 'Creating launch Secret'], ['queued', 'Queued'], ['schedule', 'Scheduling pod'],
    ['image', 'Pulling image'], ['bootstrap', 'Initializing protected storage'],
    ['connect', 'Connecting to server'], ['negotiation', 'Negotiating workspace and shell'],
    ['clone', 'Preparing workspace'], ['workspace_prepare', 'Preparing runtime readers'],
    ['engine', 'Starting agent'], ['ready', 'Ready'],
  ]
  const attempt: StartAttempt = {
    runtime: 'cluster', startedAt: Date.now(), ready: false, failed: true,
    stages: labels.map(([id, label], index) => ({
      id, label, state: index < 16 ? 'done' : index === 16 ? 'failed' : 'pending',
      ...(id === 'bootstrap' ? { detail: 'Protected storage bootstrap init container failed', hint: 'Check runtime storage and containment.' } : {}),
    })),
  }
  render(<main style={{ width: '100%', padding: 16, boxSizing: 'border-box' }}>
    <StartProgress attempt={attempt} session={{ id: 'fixture-session', status: 'error', runtime: 'cluster' }} />
  </main>)
  expect(screen.getByText('Protected storage bootstrap init container failed')).toBeVisible()
  expect(screen.getByTestId('start-hint')).toHaveTextContent('Check runtime storage and containment.')
  await userEvent.click(screen.getByRole('button', { name: 'View details' }))
  expect(screen.getAllByRole('listitem')).toHaveLength(labels.length)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  for (const row of screen.getAllByRole('listitem')) {
    expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  }
})
