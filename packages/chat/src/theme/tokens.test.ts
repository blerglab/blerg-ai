// The theming contract, enforced: every stylesheet in the package styles against --chat-* tokens
// and nothing else, so an app's theme reaches every corner. The one exception is blerg.css, whose
// whole job is to map those tokens onto the runner's palette names.
import { describe, expect, it } from 'vitest'

// The stylesheets are read from disk: vitest blanks any .css import (even ?raw) unless its css
// option is on, and the package tsconfig has no node types, so node:fs comes in by a dynamic
// import the type checker does not resolve.
interface Fs {
  readFileSync(path: string, encoding: 'utf8'): string
  readdirSync(path: string, options: { withFileTypes: true }): Array<{ name: string; isDirectory(): boolean }>
}
const fsName = 'node:fs'
const fs = (await import(/* @vite-ignore */ fsName)) as Fs
// import.meta.url is Vite's id here (/src/theme/…), not a file URL; vitest knows the real path.
const SRC = (expect.getState().testPath ?? '').replace(/\/theme\/tokens\.test\.ts$/, '/')

function cssFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = `${dir}/${entry.name}`
    if (entry.isDirectory()) out.push(...cssFiles(p))
    else if (entry.name.endsWith('.css')) out.push(p)
  }
  return out
}

// Every .css under src/, by path relative to src/ ("theme/tokens.css"), with its source.
const sheets: Record<string, string> = Object.fromEntries(
  cssFiles(SRC.replace(/\/$/, '')).map(p => [p.slice(SRC.length), fs.readFileSync(p, 'utf8')]),
)
const EXEMPT = new Set(['theme/blerg.css'])

const CONTRACT = [
  'bg', 'surface', 'surface-2', 'fg', 'muted', 'muted-2', 'accent', 'accent-fg',
  'success', 'warning', 'info', 'danger', 'border', 'radius', 'font', 'font-mono',
  'text-xs', 'text-sm', 'density',
  'card-user-bg', 'card-assistant-bg', 'card-tool-bg', 'card-system-bg',
]

describe('theme tokens', () => {
  it('finds the stylesheets', () => {
    expect(Object.keys(sheets)).toContain('theme/tokens.css')
    expect(Object.keys(sheets)).toContain('theme/blerg.css')
    expect(Object.keys(sheets)).toContain('components/markdown.css')
  })

  it.each(Object.keys(sheets))('%s uses only --chat-* custom properties', rel => {
    if (EXEMPT.has(rel)) return
    const offenders = [...sheets[rel].matchAll(/var\(\s*--(?!chat-)[\w-]*/g)].map(m => m[0])
    expect(offenders).toEqual([])
  })

  it('tokens.css declares every token of the contract with a fallback value', () => {
    const css = sheets['theme/tokens.css']
    for (const t of CONTRACT) expect(css).toMatch(new RegExp(`--chat-${t}\\s*:\\s*[^;]+;`))
  })

  it('blerg.css maps every token of the contract, in the dark and the light block', () => {
    const css = sheets['theme/blerg.css']
    for (const t of CONTRACT) {
      const hits = css.match(new RegExp(`--chat-${t}\\s*:`, 'g')) ?? []
      expect(hits.length, `--chat-${t}`).toBeGreaterThanOrEqual(2)
    }
    expect(css).toMatch(/prefers-color-scheme:\s*light/)
  })
})
