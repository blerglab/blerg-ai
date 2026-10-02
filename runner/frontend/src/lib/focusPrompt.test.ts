import { describe, it, expect } from 'vitest'
import { FOCUS_BOARD_PROMPT } from './focusPrompt'

describe('FOCUS_BOARD_PROMPT', () => {
  const p = FOCUS_BOARD_PROMPT

  it('fits the cron prompt limit', () => {
    expect(p.length).toBeGreaterThan(200)
    expect(p.length).toBeLessThanOrEqual(16384)
  })

  it('names the focus-board columns and fields', () => {
    for (const s of ['Inbox', 'Today', 'This week', 'Waiting on', 'Someday', 'Proposed', 'Done', 'source', 'due', 'tracking']) {
      expect(p).toContain(s)
    }
  })

  it('keys cards by a stable external_id so a rerun updates', () => {
    expect(p).toContain('external_id')
    expect(p.toLowerCase()).toMatch(/update/)
    expect(p.toLowerCase()).toMatch(/never (create a )?duplicate|not (create )?duplicate|instead of (creating )?(a )?duplicat/)
  })

  it('never moves a card a person moved and never deletes', () => {
    expect(p.toLowerCase()).toContain('current column')
    expect(p.toLowerCase()).toContain('recent events')
    expect(p.toLowerCase()).toMatch(/never delete/)
  })

  it('routes outward actions through propose mode only', () => {
    expect(p).toContain('propose')
    expect(p.toLowerCase()).toMatch(/approval/)
    expect(p.toLowerCase()).toMatch(/proposal id|link/)
  })

  it('treats message and event content as data', () => {
    expect(p.toLowerCase()).toContain('data')
    expect(p.toLowerCase()).toMatch(/never (act on|follow)[^.]*instructions/)
  })

  it('asks for a brief summary', () => {
    expect(p.toLowerCase()).toContain('summary')
  })

  it('names no third-party service or personal detail', () => {
    // No named third-party service, and nothing that looks like a hostname, URL, address or email.
    expect(p).not.toMatch(/gmail|google|outlook|slack/i)
    expect(p).not.toMatch(/https?:\/\/|\b[a-z0-9-]+\.(com|net|org|io|dev|local|internal|lan)\b|\b\d{1,3}(\.\d{1,3}){3}\b|\S@\S/i)
  })
})
