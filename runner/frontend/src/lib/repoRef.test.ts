import { describe, it, expect } from 'vitest'
import { parseRepoInput, predictCloneFolder } from './repoRef'

describe('parseRepoInput', () => {
  it('reads a bare owner/name on the picked provider', () => {
    expect(parseRepoInput('torvalds/linux', 'github')).toEqual({ provider: 'github', owner: 'torvalds', name: 'linux' })
    expect(parseRepoInput('  grp/tool ', 'gitlab')).toEqual({ provider: 'gitlab', owner: 'grp', name: 'tool' })
  })

  it('takes the provider from a pasted URL, whatever was picked', () => {
    for (const [text, provider, owner, name] of [
      ['https://github.com/torvalds/linux', 'github', 'torvalds', 'linux'],
      ['https://github.com/torvalds/linux.git', 'github', 'torvalds', 'linux'],
      ['https://gitlab.com/grp/tool/', 'gitlab', 'grp', 'tool'],
      ['git@github.com:octo/tiny.git', 'github', 'octo', 'tiny'],
      ['git@gitlab.com:grp/tool.git', 'gitlab', 'grp', 'tool'],
      ['ssh://git@gitlab.com/grp/tool.git', 'gitlab', 'grp', 'tool'],
      ['gitlab.com/grp/tool', 'gitlab', 'grp', 'tool'],
    ]) {
      const ref = parseRepoInput(text, 'github')
      expect(ref, text).toMatchObject({ provider, owner, name })
      expect(ref?.url, text).toBeTruthy()
    }
    expect(parseRepoInput('gitlab.com/grp/tool', 'github')?.url).toBe('https://gitlab.com/grp/tool')
  })

  it('never keeps credentials pasted inside a URL', () => {
    const ref = parseRepoInput('https://x-access-token:ghp_secret@github.com/acme/app.git', 'github')
    expect(ref).toMatchObject({ provider: 'github', owner: 'acme', name: 'app' })
    expect(ref!.url).not.toContain('ghp_secret')
    expect(ref!.url).not.toContain('@')
  })

  it('is null for anything that is not one repository on a registered host', () => {
    for (const text of [
      '', 'linux', 'a/b/c', '../x', '.hidden/x', 'x/.git', 'owner/name.git', '/abs/path',
      'https://example.com/a/b', 'https://github.com/a/b/c', 'https://github.com:8443/a/b',
      'https://github.com/a/b?tab=code', 'git@example.com:a/b.git', 'ftp://github.com/a/b',
      'git@github.com:/abs/b.git', 'has space/x', 'a\\b/c',
    ]) {
      expect(parseRepoInput(text, 'github'), text).toBeNull()
    }
  })
})

describe('predictCloneFolder', () => {
  const ref = { provider: 'github', owner: 'torvalds', name: 'linux' }
  const on = (name: string, full: string, provider?: string) => ({ name, full_name: full, provider, checked_out: ['desk'] })

  it('uses <name> when nothing is there', () => {
    expect(predictCloneFolder(ref, 'desk', [])).toEqual({ folder: 'linux', present: false })
    // A same-named folder on another daemon doesn't matter here.
    expect(predictCloneFolder(ref, 'desk', [{ name: 'linux', full_name: 'someone/linux', provider: 'github', checked_out: ['other'] }]))
      .toEqual({ folder: 'linux', present: false })
  })

  it('uses the folder that already is this repository', () => {
    expect(predictCloneFolder(ref, 'desk', [on('linux', 'Torvalds/Linux', 'github')])).toEqual({ folder: 'linux', present: true })
    expect(predictCloneFolder(ref, 'desk', [on('linux', 'linux'), on('torvalds-linux', 'torvalds/linux', 'github')]))
      .toEqual({ folder: 'torvalds-linux', present: true })
  })

  it('falls back to <owner>-<name> when <name> is another repository, saying which was left alone', () => {
    expect(predictCloneFolder(ref, 'desk', [on('linux', 'someone/linux', 'github')]))
      .toEqual({ folder: 'torvalds-linux', present: false, displaced: 'linux' })
    expect(predictCloneFolder(ref, 'desk', [on('linux', 'torvalds/linux', 'gitlab')]))
      .toEqual({ folder: 'torvalds-linux', present: false, displaced: 'linux' })
  })

  it('refuses when both folders hold something else', () => {
    const got = predictCloneFolder(ref, 'desk', [on('linux', 'linux'), on('torvalds-linux', 'x/y', 'github')])
    expect('problem' in got && got.problem).toContain('torvalds-linux')
  })
})
