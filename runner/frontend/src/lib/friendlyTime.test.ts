import { afterEach, expect, it, vi } from 'vitest'
import { longWhen } from './friendlyTime'
import { initializePersonTime } from './personTime'
import { apiFetch } from '../apiFetch'
vi.mock('../apiFetch', () => ({apiFetch: vi.fn()}))
afterEach(() => vi.resetAllMocks())
it('uses the saved person zone for today, yesterday and older dates across UTC midnight', async () => {
 vi.mocked(apiFetch).mockResolvedValue({ok:true,json:async()=>({person_time:{timezone:'America/New_York'}})} as Response)
 await initializePersonTime()
 const now = Date.parse('2026-11-06T15:00:00Z')
 expect(longWhen('2026-11-06T02:22:00Z', now)).toBe('yesterday 9:22 PM')
 expect(longWhen('2026-11-06T12:05:00Z', now)).toBe('today 7:05 AM')
 expect(longWhen('2026-11-03T00:22:00Z', now)).toBe('Nov 2, 7:22 PM')
})
it('uses the saved zone even when its calendar day differs from the browser, and refreshes it on initialization', async () => {
 vi.mocked(apiFetch).mockResolvedValue({ok:true,json:async()=>({person_time:{timezone:'Asia/Tokyo'}})} as Response)
 await initializePersonTime()
 const now = Date.parse('2026-11-06T15:00:00Z')
 expect(longWhen('2026-11-06T15:00:00Z', now)).toBe('today 12:00 AM')
 expect(longWhen('2026-11-06T12:05:00Z', now)).toBe('yesterday 9:05 PM')
})
