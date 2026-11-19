import { browserWidths } from '../test/browserWidths'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { expect, it, vi } from 'vitest'
import BranchSettingsSection from './BranchSettingsSection'
import '../index.css'
import './SettingsDialog.css'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => new Response(JSON.stringify({ branch_template: 'dev/{username}/{card}/{slug}' }))) }))
it.each(browserWidths([390, 3840]))('keeps branch settings usable at %i px', async width => {
  await page.viewport(width, 844)
  render(<main style={{ maxWidth: 680, padding: 16 }}><BranchSettingsSection /></main>)
  await userEvent.click(screen.getByText('Work branches'))
  const input = screen.getByLabelText('Branch template')
  await waitFor(() => expect(input).toBeEnabled())
  expect(input).toHaveValue('dev/{username}/{card}/{slug}')
  expect(input.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await userEvent.fill(input, 'dev/{slug}')
  await userEvent.click(screen.getByRole('button', { name: 'Save branch template' }))
  await expect.element(screen.getByRole('status')).toHaveTextContent('Saved')
})
