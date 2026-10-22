import { describe, expect, it, beforeEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { mainStep, useMainStep, useWorkingLineStore, type WorkingLineInputs } from './useWorkingLine'

const bash = { tool: 'Bash', startedAt: 1_000, detail: 'Fast-forward main' }

describe('the working line shared with the host (the Running now Main row)', () => {
  beforeEach(() => useWorkingLineStore.setState({ lines: {} }))

  it('names the call as "Tool: what it does", else what the agent is on, else Working…', () => {
    const line: WorkingLineInputs = { since: 500, waitingOn: bash, activity: null }
    expect(mainStep(line, bash)).toEqual({ since: 500, text: 'Bash: Fast-forward main', tool: 'Bash', stepSince: 1_000 })
    expect(mainStep(line, { tool: 'Read', startedAt: 2_000 }).text).toBe('Read')
    expect(mainStep({ ...line, activity: 'Thinking…' }, null).text).toBe('Thinking…')
    expect(mainStep({ ...line, waitingOn: null }, null)).toEqual({ since: 500, text: 'Working…' })
  })

  it('holds the call as the working line does: named once it has run 2 s', () => {
    useWorkingLineStore.getState().setLine('s1', { since: 0, waitingOn: bash, activity: null })
    const early = renderHook(() => useMainStep('s1', 2_500))
    expect(early.result.current?.text).toBe('Working…')
    const later = renderHook(() => useMainStep('s1', 3_000))
    expect(later.result.current?.text).toBe('Bash: Fast-forward main')
    expect(renderHook(() => useMainStep('other', 3_000)).result.current).toBeNull()
  })

  it('keeps the same state when the same line is reported again (no re-render)', () => {
    const set = useWorkingLineStore.getState().setLine
    set('s1', { since: 0, waitingOn: { ...bash }, activity: null })
    const before = useWorkingLineStore.getState().lines
    set('s1', { since: 0, waitingOn: { ...bash }, activity: null })
    expect(useWorkingLineStore.getState().lines).toBe(before)
    set('s1', null)
    expect('s1' in useWorkingLineStore.getState().lines).toBe(false)
  })
})
