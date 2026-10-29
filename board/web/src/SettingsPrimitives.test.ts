import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { settingsScreens, unstyledSettings } from '../../../packages/settings/guard'
import { buttonStyles, DESIGN_SYSTEM_BUTTONS, unstyledButtons } from '../../../packages/settings/buttons'
import { readCss } from '../../../packages/settings/readCss'

const all = import.meta.glob<string>(['./**/*.{ts,tsx}', '!./**/*.test.{ts,tsx}'], { query: '?raw', import: 'default', eager: true })
const sources = settingsScreens(ts, all, ['./BoardSettings.tsx', './Tokens.tsx'], [])

describe('shared settings primitives guard', () => {
  it.each(Object.entries(sources))('%s has no bare settings controls', (screen, source) => {
    expect(unstyledSettings(ts, source, screen)).toEqual([])
  })
  it('rejects a bare button, checkbox, radio and select with a useful message', () => {
    const failures = unstyledSettings(ts, '<><button>Save</button><input type="checkbox"/><input type="radio"/><select/></>', 'Settings.tsx')
    expect(failures).toHaveLength(4)
    expect(failures.every(f => f.includes('use the shared settings primitive'))).toBe(true)
    expect(unstyledSettings(ts, '<button className={ok ? "settings-action" : ""}>Save</button>', 'Settings.tsx')).toHaveLength(1)
  })
  it('automatically guards a newly imported settings component', () => {
    const added = settingsScreens(ts, {
      './Settings.tsx': "import NewSection from './sections/NewSection'",
      './sections/NewSection.tsx': '<button>Unstyled</button>',
    }, ['./Settings.tsx'])
    expect(() => settingsScreens(ts, {}, ['./Settings.tsx'])).toThrow('Settings guard cannot find')
    expect(added).toHaveProperty('./sections/NewSection.tsx')
    expect(unstyledSettings(ts, added['./sections/NewSection.tsx'], 'NewSection.tsx')).toHaveLength(1)
  })
})

// The button guard (#229): every button in the app is styled. CSS is what this app loads, read from disk.
const css = readCss('src', '../../packages/chat/src', '../../packages/settings')
const styles = buttonStyles(css)

describe('button guard', () => {
  it('finds no unstyled button, role="button" or input button', () => {
    expect(Object.entries(all).flatMap(([path, source]) => unstyledButtons(ts, source, path, styles))).toEqual([])
  })
  it('fails a bare button with its file, line and the approved classes', () => {
    const [failure, ...rest] = unstyledButtons(ts, '<p>\n  <button type="button" onClick={retry}>Retry models</button>\n</p>', './Fixture.tsx', styles)
    expect(rest).toEqual([])
    expect(failure).toContain('./Fixture.tsx:2: <button> has no class')
    expect(failure).toContain('use one of btn, settings-action, settings-toggle-control, artifact-btn, link-btn')
    expect(DESIGN_SYSTEM_BUTTONS.filter(c => failure.includes(c))).toEqual(['btn', 'settings-action', 'settings-toggle-control', 'artifact-btn', 'link-btn'])
  })
})
