import { describe, expect, it } from 'vitest'
import { workspaceFromCode, workspaceFromHref } from './workspacePaths'

describe('workspaceFromCode: what inline code may name', () => {
  const yes: Array<[string, { path: string; line?: number; col?: number }]> = [
    ['README.md', { path: 'README.md' }],
    ['runner/internal/server/artifacts.go:120', { path: 'runner/internal/server/artifacts.go', line: 120 }],
    ['src/a.ts:12:5', { path: 'src/a.ts', line: 12, col: 5 }],
    ['docs/a.md#L12', { path: 'docs/a.md', line: 12 }],
    ['docs/a.md#L12-L20', { path: 'docs/a.md', line: 12 }],
    ['docs/a.md:12-20', { path: 'docs/a.md', line: 12 }],
    ['./scripts/scrub.sh', { path: 'scripts/scrub.sh' }],
    ['/workspace/x/y.txt', { path: 'x/y.txt' }],
    ['/workspace/Makefile', { path: 'Makefile' }],
    ['package.json', { path: 'package.json' }],
    ['a/b/Makefile', { path: 'a/b/Makefile' }],
    ['./notes', { path: 'notes' }],
    ['docs/report.v2.md?plain=1', { path: 'docs/report.v2.md' }],
    ['docs/données.md', { path: 'docs/données.md' }],
  ]
  it.each(yes)('%s is a path', (input, want) => {
    expect(workspaceFromCode(input)).toMatchObject(want)
  })

  const no = [
    'foo.bar', 'os.Args', 'fmt.Println', 'user.name', '1.2.3', 'v1.2.3', '3.14', '42', 'main', 'useState',
    'https://example.com/a.md', 'http://x/y.go', 'mailto:a@b.co', 'me@example.com', 'ftp://h/f.txt',
    '/home/dev/x.go', '/etc/passwd', 'C:\\Users\\a\\b.txt', 'C:/Users/a.txt', '~/x.md', '../x.md', 'a/../b.md',
    'and/or', 'TCP/IP', 'input/output', 'src/components', 'src/', 'a b/c.md', 'a/b c.md', '', '   ',
    'x'.repeat(301) + '.md', 'git commit -m x', '`a.md`', 'foo.go;rm', 'a\\b.md', '.', '..', '/', '.md',
    'key=value.txt', 'a.md:abc',
  ]
  it.each(no)('%j is not', input => {
    expect(workspaceFromCode(input)).toBeNull()
  })

  it('accepts exactly 300 characters', () => {
    const p = 'a/' + 'b'.repeat(290) + '.md'
    expect(p.length).toBeLessThanOrEqual(300)
    expect(workspaceFromCode(p)?.path).toBe(p)
  })
})

describe('workspaceFromHref: markdown link targets', () => {
  it.each([
    ['README.md', { path: 'README.md' }],
    ['docs/a.md#L12', { path: 'docs/a.md', line: 12 }],
    ['./docs/a.md', { path: 'docs/a.md' }],
    ['docs/my%20file.md', { path: 'docs/my file.md' }],
    ['docs/a.md?x=1#L3', { path: 'docs/a.md', line: 3 }],
    ['/workspace/a/b.go:7', { path: 'a/b.go', line: 7 }],
    ['LICENSE', { path: 'LICENSE' }],
    ['docs/a.md#intro', { path: 'docs/a.md' }],
  ])('%s', (href, want) => {
    expect(workspaceFromHref(href)).toMatchObject(want)
  })
  it.each([
    undefined, '', '#top', 'https://x.io/a.md', 'mailto:a@b.co', 'javascript:alert(1)', 'data:text/html,x',
    '//evil.com/a.md', '/home/dev/a.md', '~/.ssh/config', '/etc/passwd', '../a.md', 'a/../../b.md', 'docs/', 'C:\\a.md',
    'a%2F..%2Fb.md', 'a%00.md', 'docs/%zz.md',
  ])('%j is not', href => {
    expect(workspaceFromHref(href)).toBeNull()
  })
  it('does not treat file.go:12 as a scheme', () => {
    expect(workspaceFromHref('main.go:12')).toMatchObject({ path: 'main.go', line: 12 })
  })
})
