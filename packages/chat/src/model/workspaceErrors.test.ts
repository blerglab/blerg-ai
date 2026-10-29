import { describe, expect, it } from 'vitest'
import { workspaceErrorText } from './workspaceErrors'

describe('workspaceErrorText: the sentence for a refused read', () => {
  it.each([
    [404, 'The workspace file was not found.', 'Not found in the session’s folder'],
    [404, 'Session not found.', 'This session can’t be found'],
    [403, 'Blerg doesn\'t open files that usually hold secrets (restricted filename).', 'Blerg doesn’t open files that usually hold secrets'],
    [403, 'Blerg could not safely open that workspace file.', 'Blerg can’t open that path'],
    [403, 'Only the session owner or an administrator can open workspace files.', 'Ask the session owner'],
    [409, 'The session has no live workspace engine.', 'The session isn’t running, so its files can’t be read'],
    [409, 'The session has no live workspace daemon.', 'The session isn’t running, so its files can’t be read'],
    [409, 'The workspace file reader is busy.', 'busy'],
    [409, 'The session daemon did not provide a workspace file.', 'Try again'],
    [413, 'The workspace file is larger than its configured size limit.', 'Too large to open here'],
    [401, 'unauthorized', 'Sign in again'],
    [500, 'secret internal detail', 'Couldn’t open that file'],
  ])('%i %s', (status, text, want) => {
    const got = workspaceErrorText(status, text)
    expect(got).toContain(want)
    expect(got).not.toContain('internal detail')
  })
})

it('explains ownership refusals without showing server details', () => {
  const message = workspaceErrorText(403, 'secret internal detail', 'desktop_ownership')
  expect(message).toContain('Your desktops')
  expect(message).toContain('enroll')
  expect(message).not.toContain('internal detail')
})

it.each([
  ['session_ownership', 'Ask the session owner'],
  ['person_required', 'Sign in as a person'],
])('explains %s with a recovery action', (code, action) => {
  const message = workspaceErrorText(403, 'private internal detail', code)
  expect(message).toContain(action)
  expect(message).not.toContain('private internal detail')
  expect(message).not.toContain('Not found')
})
