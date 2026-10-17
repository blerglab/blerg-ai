import { describe, expect, it } from 'vitest'
import { langFromInfo, langFromName } from './codeLang'

describe('codeLang', () => {
  it('maps fence info strings', () => {
    expect(langFromInfo('language-tsx')).toBe('typescript')
    expect(langFromInfo('go title=x')).toBe('go')
    expect(langFromInfo('SH')).toBe('bash')
    expect(langFromInfo('card:ticket')).toBeNull()
    expect(langFromInfo('nope')).toBeNull()
    expect(langFromInfo('constructor')).toBeNull()
    expect(langFromInfo(undefined)).toBeNull()
  })
  it('maps file names by extension', () => {
    expect(langFromName('main.go')).toBe('go')
    expect(langFromName('a/b.test.TS')).toBe('typescript')
    expect(langFromName('Makefile')).toBeNull()
    expect(langFromName('notes.txt')).toBeNull()
  })
})
