import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, it } from 'vitest'

it('L4: documents the additive chat popover release', () => {
  const changelog = readFileSync(resolve('../../CHANGELOG.md'), 'utf8')
  expect(changelog).toMatch(/@blerglab\/chat`? 1\.27\.0/)
  expect(changelog).toContain('placement="anchor"')
})
// The artifact check runs with the capture directory supplied by the release builder.
// Browser capture cases exercise the same six states in every regular browser run.
it.skipIf(!process.env.SHELL_TAB_SHOTS_DIR).each([390, 3840].flatMap(width => ['single', 'split', 'light'].map(mode => ({ width, mode }))))('L5: delivers a full-size $mode capture at $width', ({ width, mode }) => {
  const png = readFileSync(resolve(process.env.SHELL_TAB_SHOTS_DIR!, `shell-${mode}-${width}.png`))
  expect(png.subarray(1,4).toString()).toBe('PNG')
  expect(png.readUInt32BE(16)).toBe(width)
  expect(png.readUInt32BE(20)).toBe(width === 390 ? 844 : 2160)
  expect(png.length).toBeGreaterThan(10_000)
})
