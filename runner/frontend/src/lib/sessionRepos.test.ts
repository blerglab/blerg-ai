import { expect, it } from 'vitest'
import { makeSession } from '../test/fixtures'
import { sessionRepos } from './sessionRepos'

it('names single and multiple launch members and keeps legacy sessions readable', () => {
  expect(sessionRepos(makeSession({ repo: 'workspace', workspace: { members: [{ name: 'blerg', path: '.' }] } })))
    .toEqual({ label: 'blerg', full: 'blerg' })
  expect(sessionRepos(makeSession({ repo: 'workspace', workspace: { members: ['blerg', 'board', 'chat'].map(name => ({ name, path: name })) } })))
    .toEqual({ label: 'blerg + 2', full: 'blerg, board, chat' })
  expect(sessionRepos(makeSession({ repo: 'example' }))).toEqual({ label: 'example', full: 'example' })
  expect(sessionRepos(makeSession({ repo: '' }))).toEqual({ label: 'No repository', full: 'No repository' })
})
