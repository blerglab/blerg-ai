import { describe, expect, it } from 'vitest'
import { createCardRegistry, defineCard } from './registry'
import type { CardProps } from './registry'

function Character({ data }: CardProps) {
  return <div>{String(data.name)}</div>
}

describe('card registry', () => {
  it('finds a defined kind and nothing else', () => {
    const reg = createCardRegistry([defineCard('character', Character)])
    expect(reg.get('character')?.kind).toBe('character')
    expect(reg.get('character')?.Component).toBe(Character)
    expect(reg.get('scene')).toBeUndefined()
  })
  it('never answers with an Object.prototype member for a kind the model made up', () => {
    const reg = createCardRegistry([])
    for (const k of ['constructor', '__proto__', 'toString', 'hasOwnProperty']) expect(reg.get(k)).toBeUndefined()
  })
  it('lets a later definition of the same kind win', () => {
    const Other = () => <div>other</div>
    const reg = createCardRegistry([defineCard('x', Character), defineCard('x', Other)])
    expect(reg.get('x')?.Component).toBe(Other)
  })
})
