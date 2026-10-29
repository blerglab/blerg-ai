import { expect, it } from 'vitest'
import { workspaceStatusReason } from './workspaceStatusReason'
it('accepts only fixed status diagnostics and hides raw upstream text', () => {
  expect(workspaceStatusReason('metadata_refused')).toContain('Git metadata failed its safety check')
  for (const value of [null, {}, 'constructor', 'private/path stderr secret']) expect(workspaceStatusReason(value)).toBeUndefined()
})
