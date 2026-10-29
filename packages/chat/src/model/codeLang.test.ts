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
    expect(langFromName('Makefile')).toBe('makefile')
    expect(langFromName('notes.txt')).toBeNull()
  })
})

it.each(['Dockerfile', 'Dockerfile.dev', 'build.dockerfile', 'src/Dockerfile.prod'])('detects Dockerfile name %s', name => {
  expect(langFromName(name)).toBe('dockerfile')
})
it.each(['Makefile', 'GNUmakefile', 'rules.mk', 'src/Makefile'])('detects Makefile name %s', name => {
  expect(langFromName(name)).toBe('makefile')
})
it.each([['go.mod', 'gomod'], ['go.sum', 'gosum']])('detects Go manifest %s', (name, lang) => {
  expect(langFromName(name)).toBe(lang)
})
