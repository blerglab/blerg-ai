import { browserWidths } from '../test/browserWidths'
import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { commands, page } from 'vitest/browser'
import { setTheme } from '../../../../packages/settings/theme'
import SessionFailureNotice from './SessionFailureNotice'
import '../index.css'

it.each(browserWidths([390, 3840]))('explains a disconnected pod and recovery at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const reason = 'stopped: ran out of memory (limit 4 GiB; OOMKilled). Ask an administrator to raise BLERG_RUNNER_POD_MEM_LIMIT in install/k8s/.env and redeploy; the new limit applies on resume. A retained workspace volume may hold files and unpublished commits from the interrupted turn. Recovery is not yet verified. Resume reuses the retained checkout and refuses to continue if its storage is missing or cannot be verified. Files outside that volume may be lost.'
  const { rerender } = render(<SessionFailureNotice status="disconnected" reason={reason} />)
  expect(screen.getByRole('status')).toHaveTextContent(reason)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  const shots = import.meta.env.VITE_FAILURE_NOTICE_SHOTS
  if (shots) {
    for (const theme of ['light', 'colorblind-dark'] as const) {
      setTheme(theme)
      await commands.layoutScreenshot(width, theme, 'workspace')
    }
    setTheme('light')
  }
  rerender(<SessionFailureNotice status="running" reason={reason} />)
  expect(screen.queryByRole('status')).toBeNull()
})
