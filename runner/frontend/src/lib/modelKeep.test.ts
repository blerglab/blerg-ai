import { describe, expect, it } from 'vitest'
import { keepNotice, matchModel, modelFamily, placeWords } from './modelKeep'
import type { EngineModel } from '../types'

const M = (id: string, name: string, efforts = ['low', 'high'], section: 'main' | 'overflow' = 'main'): EngineModel => ({ id, name, description: '', section, efforts, default_effort: efforts[0] })

describe('keeping a model across places (card #151)', () => {
  const list = [M('claude-opus-5', 'Opus 5', ['low']), M('claude-sonnet-5-5', 'Sonnet 5.5')]
  it('matches by routing id, then display name, then family', () => {
    expect(matchModel({ id: 'claude-sonnet-5-5' }, list)).toMatchObject({ by: 'id', model: { id: 'claude-sonnet-5-5' } })
    expect(matchModel({ id: 'claude-sonnet-5-5-20261001', name: 'Sonnet 5.5' }, list)).toMatchObject({ by: 'name', model: { id: 'claude-sonnet-5-5' } })
    expect(matchModel({ id: 'claude-opus-5-5', name: 'Opus 5.5' }, list)).toMatchObject({ by: 'family', model: { id: 'claude-opus-5' } })
    expect(matchModel({ id: 'claude-haiku-5', name: 'Haiku 5' }, list)).toBeNull()
    expect(modelFamily({ id: 'gpt-9-codex', name: 'GPT-9 Codex' })).toBe('gpt-9')
  })
  it('says what changed: a different model (family or none), an effort not offered; nothing for the same model', () => {
    const place = placeWords('cluster')
    expect(keepNotice({ pick: { id: 'claude-opus-5-5', name: 'Opus 5.5' }, match: matchModel({ id: 'claude-opus-5-5', name: 'Opus 5.5' }, list), selected: list[0], explicitEffort: 'high', effort: 'low', place }))
      .toBe("Opus 5.5 isn't available in the cluster: switched to Opus 5. high effort isn't available for Opus 5: using low")
    expect(keepNotice({ pick: { id: 'claude-haiku-5', name: 'Haiku 5' }, match: null, selected: list[1], explicitEffort: null, effort: 'low', place }))
      .toBe("Haiku 5 isn't available in the cluster: switched to Sonnet 5.5")
    expect(keepNotice({ pick: { id: 'x', name: 'Sonnet 5.5' }, match: { by: 'name', model: list[1] }, selected: list[1], explicitEffort: 'high', effort: 'high', place })).toBeNull()
    expect(placeWords('docker')).toBe('in the local sandbox')
    expect(placeWords('daemon', 'workstation')).toBe('on workstation')
  })
})
