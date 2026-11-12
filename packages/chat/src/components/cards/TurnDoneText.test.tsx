import { expect, it } from 'vitest'
import { render } from '@testing-library/react'
import { turnDoneText } from './EventCards'
import type { AgentEvent } from '../../types'

it.each([
  ['end_turn', 'Turn finished'],
  ['interrupted', 'Interrupted'],
  ['error', 'Failed'],
  ['max_tokens', 'Response limit reached'],
  ['provider_specific_reason', 'Turn ended'],
  ['__proto__', 'Turn ended'],
  ['', 'Turn ended'],
])('describes %s in plain words in the transcript footer', (reason, label) => {
  const event = { ts: '', payload: { stop_reason: reason, model: 'claude-sonnet-5', usage: { input_tokens: 12, output_tokens: 3 } } } as AgentEvent
  const { container } = render(<div>{turnDoneText(event)}</div>)
  expect(container.textContent?.split(' · ').slice(0, 4)).toEqual([label, 'sonnet-5', '12 in', '3 out'])
})
