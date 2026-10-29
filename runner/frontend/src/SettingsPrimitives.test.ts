import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { settingsScreens, unstyledSettings } from '../../../packages/settings/guard'
import { buttonStyles, DESIGN_SYSTEM_BUTTONS, unstyledButtons } from '../../../packages/settings/buttons'
import { readCss } from '../../../packages/settings/readCss'

const all = import.meta.glob<string>(['./**/*.{ts,tsx}', '!./**/*.test.{ts,tsx}'], { query: '?raw', import: 'default', eager: true })
const sources = settingsScreens(ts, all, ['./components/SettingsDialog.tsx'], [])

describe('shared settings primitives guard', () => {
  it.each(Object.entries(sources))('%s has no bare settings controls', (screen, source) => {
    expect(unstyledSettings(ts, source, screen)).toEqual([])
  })
  it.each(['RunColumn', 'ModelPicker', 'ReposRootEditor', 'LaunchPermissions', 'McpPicker'])('uses shared controls for launch settings in %s', name => {
    const path = `./components/${name}.tsx`
    expect(unstyledSettings(ts, all[path], path)).toEqual([])
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
    expect(failure).toContain('use one of btn, icon-btn, settings-action, settings-toggle-control, artifact-btn')
    expect(DESIGN_SYSTEM_BUTTONS.filter(c => failure.includes(c))).toEqual(['btn', 'icon-btn', 'settings-action', 'settings-toggle-control', 'artifact-btn'])
  })
  it('rejects unknown classes, unstyled branches, partial inline styles and reasonless opt-outs', () => {
    const fail = (src: string) => unstyledButtons(ts, src, './F.tsx', styles)
    expect(fail('<button className="retry-link">Retry</button>')[0]).toContain('has no approved button class (found "retry-link")')
    expect(fail('<a role="button" href="#">Open</a>')).toHaveLength(1)
    expect(fail('<><input type="submit" /><input type="button" className="btn" /><input type="text" /></>')).toHaveLength(1)
    expect(fail('<button className={busy ? "btn" : "pending"}>Save</button>')).toHaveLength(1)
    expect(fail('<button className={`${tone}-action`}>Save</button>')).toHaveLength(1)
    expect(fail('<button style={{ marginLeft: 6 }}>Retry</button>')).toHaveLength(1)
    expect(fail('// button-guard-ignore:\n<button>Retry</button>')).toHaveLength(1)
  })
  it('accepts design-system and component classes, styled containers, full inline styles and reviewed opt-outs', () => {
    const pass = (src: string, sheet = styles) => expect(unstyledButtons(ts, src, './F.tsx', sheet)).toEqual([])
    pass('<button className="btn small">Retry</button>')
    pass('<button className={busy ? "btn" : "btn primary"}>Save</button>')
    pass('const chip = reel ? "icon-btn" : "artifact-btn"\nconst x = <button className={`${chip}${on ? " on" : ""}`}>A</button>')
    pass('const props = { className: "settings-action" }\nconst x = <button {...props}>A</button>')
    pass('const base = { background: "none", border: 0 }\nconst x = <button style={{ ...base, color: "red" }}>A</button>')
    pass('const pill = (on: boolean) => ({ background: on ? "a" : "b", border: "1px solid" })\nconst x = <button style={pill(true)}>A</button>')
    pass('<div className="bar"><span><button>A</button></span></div>', buttonStyles({ 'a.css': '.bar button { background: none; border: 0 }' }))
    pass('<>\n  {/* button-guard-ignore: drawn on the canvas by the editor */}\n  <button>A</button>\n</>')
  })
  it('derives button classes from plain CSS rules only', () => {
    const sheet = buttonStyles({ 'a.css': '.hover:hover { background: none; border: 0 } .pair.on { background: none; border: 0 } .half { border: 0 } @media (max-width: 1px) { .media { background: none; border: 0 } } .plain { background: none; border: 0 }' })
    const check = (cls: string) => unstyledButtons(ts, `<button className="${cls}">A</button>`, './F.tsx', sheet).length
    expect([check('hover'), check('pair'), check('pair on'), check('half'), check('media'), check('plain')]).toEqual([1, 1, 0, 1, 1, 0])
  })

  // Review of #229: four ways the first guard could be bypassed. Each probe was accepted by it.
  const fails = (src: string, sheet = styles) => expect(unstyledButtons(ts, src, './F.tsx', sheet), src).toHaveLength(1)
  const passes = (src: string, sheet = styles) => expect(unstyledButtons(ts, src, './F.tsx', sheet), src).toEqual([])
  it('counts a CSS rule only when its whole selector provably matches the control at rest', () => {
    const sheet = (rule: string) => buttonStyles({ 'a.css': rule })
    fails('<button className="action">Retry</button>', sheet('.toolbar .action { background: none; border: 0 }'))
    passes('<div className="toolbar"><button className="action">Retry</button></div>', sheet('.toolbar .action { background: none; border: 0 }'))
    fails('<div className="toolbar"><span><button>Retry</button></span></div>', sheet('.toolbar.active > button { background: none; border: 0 }'))
    fails('<div className="toolbar"><span><button>Retry</button></span></div>', sheet('.toolbar > button { background: none; border: 0 }'))
    passes('<div className="toolbar"><button>Retry</button></div>', sheet('.toolbar > button { background: none; border: 0 }'))
    fails('<div className="toolbar"><Hover><button>Retry</button></Hover></div>', sheet('.toolbar > button { background: none; border: 0 }'))
    fails('<div className="toolbar"><button>Retry</button></div>', sheet('.toolbar:hover button { background: none; border: 0 }'))
    fails('<div className="toolbar"><button>Retry</button></div>', sheet('.toolbar input[type=text] { background: none; border: 0 }'))
    fails('<button className="action">Retry</button>', sheet('@media (min-width:9999px) { .action { background:none; border:0 } }'))
    fails('<button className="action">Retry</button>', sheet('.action { appearance: auto }'))
    fails('<button className="action">Retry</button>', sheet('.action { background: none; border: 0 } .action { all: revert }'))
    passes('<button className="action">Retry</button>', sheet('.action { all: unset }'))
    fails('<button className="action">Retry</button>', sheet('.action { background: revert; border: 0 }'))
    // Repository CSS: the Answer button's styling is scoped to the rail.
    fails('<button className="ask-reply-send">Answer</button>')
    passes('<div className="rail-reply"><button className="ask-reply-send">Answer</button></div>')
  })
  it('counts an inline style only for known values that set the background and the border', () => {
    fails('<button style={{}}>Retry</button>')
    fails('<button style={{ background: undefined, border: undefined }}>Retry</button>')
    fails("<button style={{ background: '', border: '' }}>Retry</button>")
    fails("<button style={{ appearance: 'auto' }}>Retry</button>")
    fails("<button style={{ background: on ? 'none' : undefined, border: 0 }}>Retry</button>")
    fails("<button style={{ background: 'none', border: 0, ...{ background: undefined } }}>Retry</button>")
    fails("<button style={{ background: color, border: 0 }}>Retry</button>")
    fails("<button className=\"btn\" style={{ background: 'initial' }}>Retry</button>")
    passes("<button style={{ background: 'none', border: 0 }}>Retry</button>")
    passes("const base = { background: 'none', border: '1px solid' }\nconst x = <button style={{ ...base, color: 'red' }}>Retry</button>")
  })
  it('reads props left to right and fails closed on what it cannot resolve', () => {
    fails('const props = { className: undefined }\nconst x = <button className="btn" {...props}>Retry</button>')
    fails('const x = <button className="btn" {...rest}>Retry</button>')
    passes('const x = <button {...rest} className="btn">Retry</button>')
    fails("const kind = 'submit'\nconst x = <input type={kind} />")
    fails("const props = { type: 'submit' }\nconst x = <input {...props} />")
    fails('const x = <input {...rest} />')
    fails("const props = { role: 'button' }\nconst x = <a {...props}>Retry</a>")
    fails("function link() { return { role: 'button' as const, tabIndex: 0 } }\nconst x = <span {...link()}>Retry</span>")
    fails('function Chip({ cls }: { cls: string }) { return <button className={cls}>A</button> }')
    fails("const cls = 'btn'\nfunction Chip({ cls }: { cls: string }) { return <button className={cls}>A</button> }")
    passes("const kind = 'text'\nconst x = <input type={kind} />")
  })
  it('accepts only a real comment with a reason, alone on the line above', () => {
    fails('<button data-note="button-guard-ignore:" type="button">Retry</button>')
    fails('<button title="button-guard-ignore: tooltip">Retry</button>')
    fails('<>\n  {/* button-guard-ignore: */}<span />\n  <button>Retry</button>\n</>')
    fails('<>\n  {/* button-guard-ignore: reason */}<span />\n  <button>Retry</button>\n</>')
    fails('<>\n  <p>// button-guard-ignore: text, not a comment</p>\n  <button>Retry</button>\n</>')
    fails('// button-guard-ignore:\n<button>Retry</button>')
    fails('// button-guard-ignore: two lines up\n\n<button>Retry</button>')
    passes('<>\n  {/* button-guard-ignore: drawn on the canvas by the editor */}\n  <button>Retry</button>\n</>')
    passes('const x = (\n  // button-guard-ignore: drawn on the canvas by the editor\n  <button>Retry</button>\n)')
  })
})
