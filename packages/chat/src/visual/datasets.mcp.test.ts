import {expect, it} from 'vitest'
import {validDataset} from './datasets'

it('accepts only an opaque binding reference and never a connection credential or argument', () => {
  const source = {name: 'blerg.mcp', parameters: {binding: '00000000-0000-4000-8000-000000000514'}}
  expect(validDataset(source)).toBe(true)
  for (const parameters of [{}, {binding: 'unknown'}, {...source.parameters, token: 'canary'}, {...source.parameters, tool: 'quotes'}, {...source.parameters, arguments: '{}'}, {...source.parameters, url: 'https://market.example'}]) {
    expect(validDataset({...source, parameters})).toBe(false)
  }
})
