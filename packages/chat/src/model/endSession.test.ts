import { describe, expect, it } from 'vitest'
import { endSessionText } from './endSession'

// Card #142: the End session confirmation says what is kept, accurately for where the session runs.
describe('endSessionText', () => {
  it('keeps the conversation and files on this machine', () => {
    expect(endSessionText('daemon')).toBe('The agent stops. The conversation and files are kept.')
  })
  it('says a local sandbox loses its container but keeps files and commits', () => {
    expect(endSessionText('docker')).toBe('The agent stops and its sandbox container is removed. The conversation, files and commits are kept.')
  })
  it('says a cluster pod is removed and unpushed work in it is lost', () => {
    expect(endSessionText('cluster')).toMatch(/pod is removed.*not pushed is lost/)
  })
  it('promises nothing it cannot know when the runtime is unknown', () => {
    expect(endSessionText(undefined)).toBe('The agent stops. Work not yet saved may be lost; the conversation is kept.')
    expect(endSessionText('')).toBe('The agent stops. Work not yet saved may be lost; the conversation is kept.')
  })
  it('words a terminal session as a terminal, with no conversation', () => {
    expect(endSessionText('daemon', true)).toBe('The terminal session stops. Files are kept.')
    expect(endSessionText('docker', true)).toMatch(/^The terminal session stops and its sandbox container is removed/)
    expect(endSessionText('cluster', true)).toMatch(/^The terminal session stops and its pod is removed/)
    expect(endSessionText(undefined, true)).toBe('The terminal session stops. Work not yet saved may be lost.')
    for (const r of ['daemon', 'docker', 'cluster', undefined]) expect(endSessionText(r, true)).not.toMatch(/conversation|agent/)
  })
})
