import { afterEach, expect, it, vi } from 'vitest'
import { blackboardTargetDate } from './blackboardDate'
afterEach(()=>vi.unstubAllEnvs())
it('keeps the recorded target day for a viewer west of UTC',()=>{
 vi.stubEnv('TZ','America/New_York')
 expect(blackboardTargetDate('2026-11-05T00:00:00Z','en-US')).toBe('Nov 5')
})
it('keeps the recorded target day for a viewer east of UTC',()=>{
 vi.stubEnv('TZ','Pacific/Kiritimati')
 expect(blackboardTargetDate('2026-11-05T00:00:00Z','en-US')).toBe('Nov 5')
})
