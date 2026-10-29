import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { apiFetch } from '../apiFetch'
import UsageLimitResume from './UsageLimitResume'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
afterEach(() => vi.resetAllMocks())

for (const width of [390, 3840]) {
  it(`shows the meter wait at ${width}px`, async () => {
    await page.viewport(width, 900)
    vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({
      enabled: true, state: 'unknown', reason: '', attempts: 0,
    })))
    render(<UsageLimitResume sessionId={`meter-wait-${width}`} />)
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Reset time unknown; checking the usage meter.'))
    const rect = screen.getByRole('status').getBoundingClientRect()
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(width)
    expect(screen.getByRole('checkbox')).toBeChecked()
  })
}
