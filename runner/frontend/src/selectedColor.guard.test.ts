// Card #128, kept: an on / open / pressed / selected state is drawn with the selected blue
// (--selected-*, --chat-selected-*), never the accent (--accent*, --blaze*, --chat-accent*), which next
// to the danger red read as a warning. This scans every stylesheet of the runner, the board, core's
// web app, the chat package and the brand tokens for rules whose selector names such a state and whose
// declarations use an accent token. A deliberate exception goes in ALLOWED with its reason.
import { describe, expect, it } from 'vitest'

// The app's tsconfig has no Node types: the file system comes in untyped, through a dynamic import.
interface NodeFs {
  readFileSync(p: string, enc: 'utf8'): string
  readdirSync(p: string, o: { withFileTypes: true }): Array<{ name: string; isDirectory(): boolean }>
}
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const fs = (await nodeModule('node:fs')) as NodeFs
// import.meta.url is Vite's id here; vitest knows the real path of this file.
const REPO = (expect.getState().testPath ?? '').replace(/\/runner\/frontend\/src\/selectedColor\.guard\.test\.ts$/, '')

const ROOTS = ['runner/frontend/src', 'board/web/src', 'core/web/src', 'packages/chat/src', 'brand']

// "file :: selector" → why the accent is right there.
const ALLOWED: Record<string, string> = {
  'brand/tokens.css :: .chip.active': 'a status chip ("active" = running work), not a selection; the dot carries the state',
  'brand/tokens.css :: .chip.active .dot': 'the status chip\'s running dot',
}

const STATE = /\[aria-(?:selected|pressed|expanded)=["']?true["']?\]|\[aria-current(?:=[^\]]*)?\]|\.(?:active|selected|on)\b|:checked\b/
const ACCENT = /var\(\s*--(?:accent|blaze|chat-accent)[\w-]*/

function cssFiles(dir: string): string[] {
  const out: string[] = []
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name.startsWith('.')) continue
    const p = `${dir}/${e.name}`
    if (e.isDirectory()) out.push(...cssFiles(p))
    else if (e.name.endsWith('.css')) out.push(p)
  }
  return out
}

// Innermost rules (selector { declarations }), comments removed; @media wrappers fall away because
// only blocks without a nested brace are matched.
function offenders(): string[] {
  const found: string[] = []
  for (const root of ROOTS) {
    for (const file of cssFiles(`${REPO}/${root}`)) {
      const rel = file.slice(REPO.length + 1)
      const css = fs.readFileSync(file, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
      for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
        if (!ACCENT.test(m[2])) continue
        for (const sel of m[1].split(',').map(s => s.trim().replace(/\s+/g, ' '))) {
          if (STATE.test(sel)) found.push(`${rel} :: ${sel}`)
        }
      }
    }
  }
  return found
}

describe('selected states', () => {
  it('finds the stylesheets', () => {
    expect(cssFiles(`${REPO}/runner/frontend/src`).length).toBeGreaterThan(5)
    expect(cssFiles(`${REPO}/packages/chat/src`).length).toBeGreaterThan(5)
  })

  it('never draw an on / open / pressed / selected state with the accent', () => {
    expect(offenders().filter(o => !(o in ALLOWED))).toEqual([])
  })

  it('keeps the allowlist honest: every entry still exists', () => {
    const found = new Set(offenders())
    expect(Object.keys(ALLOWED).filter(k => !found.has(k))).toEqual([])
  })
})
