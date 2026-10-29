import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { buttonStyles, unstyledButtons } from '../../settings/buttons'
import { readCss } from '../../settings/readCss'

// The button guard (#229) over the chat package, against the chat's own CSS: a chat button must look
// right in every app that renders it. Settings primitives and the runner/board guards live in each app.
const all = import.meta.glob<string>(['./**/*.{ts,tsx}', '!./**/*.test.{ts,tsx}'], { query: '?raw', import: 'default', eager: true })
const css = readCss('src')
const styles = buttonStyles(css)

describe('button guard', () => {
  it('finds no unstyled button, role="button" or input button', () => {
    expect(Object.entries(all).flatMap(([path, source]) => unstyledButtons(ts, source, path, styles))).toEqual([])
  })
  it('fails a bare button with its file, line and the approved classes', () => {
    const [failure, ...rest] = unstyledButtons(ts, '<form>\n  <button type="submit">Answer</button>\n</form>', './Fixture.tsx', styles)
    expect(rest).toEqual([])
    expect(failure).toContain('./Fixture.tsx:2: <button> has no class')
    expect(failure).toContain('use one of artifact-btn')
  })
})
