import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import SessionFailureNotice from './SessionFailureNotice'
import '../index.css'

it.each([390, 3840])('explains a disconnected pod and recovery at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const reason = 'stopped: ran out of memory (limit 4 GiB; OOMKilled). Ask an administrator to raise BLERG_RUNNER_POD_MEM_LIMIT in install/k8s/.env and redeploy; the new limit applies on resume. Work in the retained workspace volume, including commits from an interrupted turn, is restored when you resume this session.'
  const { rerender } = render(<SessionFailureNotice status="disconnected" reason={reason} />)
  expect(screen.getByRole('status')).toHaveTextContent(reason)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  rerender(<SessionFailureNotice status="running" reason={reason} />)
  expect(screen.queryByRole('status')).toBeNull()
})
