import { expect, it } from 'vitest'
import { questionPresentation } from './questionPresentation'

it('separates server-quoted asker and parent names without changing the source message', () => {
  const body = `Child "Fix 'answers'", parent "Main": Which approach?\nKeep the payload intact.`
  const original = body
  expect(questionPresentation(body)).toEqual({ text: 'Which approach?\nKeep the payload intact.', asker: "Fix 'answers'", parent: 'Main' })
  expect(body).toBe(original)
  expect(questionPresentation('Child "Fix C:\\repo", parent "Main": Pick one?')).toEqual({ text: 'Pick one?', asker: 'Fix C:\\repo', parent: 'Main' })
})
it('keeps empty and ordinary text, with a worker or Main as asker', () => {
  expect(questionPresentation('')).toEqual({ text: '', asker: 'Main', parent: undefined })
  expect(questionPresentation('Pick one?', 'Ada')).toEqual({ text: 'Pick one?', asker: 'Ada', parent: undefined })
  expect(questionPresentation('Child "broken, parent "Main": Pick one?').text).toContain('Child')
})

it('shows the named worker and Main from the current escalation envelope', () => {
  expect(questionPresentation('Worker "Release checks", via "Main": Publish now?')).toEqual({ text: 'Publish now?', asker: 'Release checks', parent: 'Main' })
})
