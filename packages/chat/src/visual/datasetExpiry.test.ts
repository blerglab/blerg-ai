import {expect,it} from 'vitest'
import {mcpSampleExpiry} from './datasets'

it('caps cached MCP rows independently by authority deadline and 24-hour retention',()=>{
 const observed_at='2026-11-05T12:00:00Z'
 expect(mcpSampleExpiry({observed_at,valid_until:'2026-11-05T12:00:02Z'})).toBe(Date.parse('2026-11-05T12:00:02Z'))
 expect(mcpSampleExpiry({observed_at,valid_until:'2026-11-07T12:00:00Z'})).toBe(Date.parse('2026-11-06T12:00:00Z'))
})
it.each([{observed_at:'',valid_until:'2026-11-05T13:00:00Z'},{observed_at:'2026-11-05T12:00:00Z'},{observed_at:'2026-11-05T12:00:00Z',valid_until:'invalid'}])('refuses missing or invalid MCP time bounds %j',sample=>{expect(mcpSampleExpiry(sample)).toBe(0)})
