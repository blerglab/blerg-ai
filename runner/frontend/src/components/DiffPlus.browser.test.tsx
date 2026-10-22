// The diff's [+] for a new comment, GitHub's way (#135), in real Chromium at 1280 px, 3840 px and on
// a phone: one [+] per row in unified view, in one spot right of both line-number columns and on
// the first line of a wrapped line; in split view one [+] per side, shown for the side under the
// pointer and commenting on that side's line.
import {afterEach, describe, expect, it} from 'vitest'
import {render} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {DiffView, type Anchor} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const LONG = `const longLine = "${'wrapped words '.repeat(300)}"`
// Old lines 10–13, new lines 20–23: a context line, a removal, an addition, a long context line.
const PATCH = `diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\n@@ -10,4 +20,4 @@\n const keep = 1\n-const gone = 2\n+const fresh = 3\n ${LONG}\n tail\n`

afterEach(async () => { await page.viewport(1280, 800) })

function mount() {
  const anchors: Anchor[] = []
  // The review's wrapper: long lines wrap there, as in the diff review.
  const view = render(<div className="review-mode review-inline-mode"><DiffView text={PATCH} onComment={a => anchors.push(a)} onSelection={() => {}} /></div>)
  return {...view, anchors}
}

const rowOf = (container: HTMLElement, text: string) =>
  [...container.querySelectorAll<HTMLTableRowElement>('tr.diff-row')].find(r => r.textContent?.includes(text))!
const plusIn = (el: Element) => [...el.querySelectorAll<HTMLButtonElement>('.diff-comment-add')]
const where = (a: Anchor) => `${a.diff!.side} ${a.diff!.start}`

describe('the [+] in unified view', () => {
  it.each([[1280, 800], [3840, 2160], [390, 844]])('is one per row, in one spot, top-aligned, anchored to the right line at %ipx', async (width, height) => {
    await page.viewport(width, height)
    const {container, anchors} = mount()
    expect(container.querySelector('[data-testid="diff-table-unified"]')).not.toBeNull()
    const rows = {keep: rowOf(container, 'const keep'), gone: rowOf(container, 'const gone'), fresh: rowOf(container, 'const fresh'), long: rowOf(container, 'longLine')}
    for (const row of Object.values(rows)) expect(plusIn(row)).toHaveLength(1)
    // Never in a line-number column: in the code cell, right of both numbers.
    const lefts = Object.values(rows).map(row => {
      const plus = plusIn(row)[0]
      expect(plus.closest('td')!.classList.contains('diff-code')).toBe(true)
      const numbers = [...row.querySelectorAll('td.diff-num')].map(td => td.getBoundingClientRect().right)
      expect(plus.getBoundingClientRect().left).toBeGreaterThanOrEqual(Math.max(...numbers) - 0.5)
      return plus.getBoundingClientRect().left
    })
    expect(Math.max(...lefts) - Math.min(...lefts)).toBeLessThan(0.5)
    // A wrapped line: the [+] sits on the first line, level with the line number.
    const longRow = rows.long
    expect(longRow.getBoundingClientRect().height).toBeGreaterThan(30)
    const number = longRow.querySelector('.diff-line-select[data-diff-side="new"]')!.getBoundingClientRect()
    expect(Math.abs(plusIn(longRow)[0].getBoundingClientRect().top - number.top)).toBeLessThan(3)
    // Added and unchanged lines comment on their new line, a removed line on its old line.
    for (const row of [rows.keep, rows.fresh, rows.gone, rows.long]) await userEvent.click(plusIn(row)[0], {force: true})
    expect(anchors.map(where)).toEqual(['new 20', 'new 21', 'old 11', 'new 22'])
  })
})

describe('the [+] in split view', () => {
  it.each([[1280, 800], [3840, 2160]])('shows one [+], on the side under the pointer, for that side\'s line, at %ipx', async (width, height) => {
    await page.viewport(width, height)
    const {container, anchors, getByRole} = mount()
    await userEvent.click(getByRole('button', {name: 'Side by side'}))
    const visible = (row: Element) => plusIn(row).filter(b => getComputedStyle(b).opacity === '1')
    // A clicked [+] keeps focus (and so stays shown, for the keyboard); the pointer test moves on.
    const blur = () => (document.activeElement as HTMLElement | null)?.blur()
    const keep = rowOf(container, 'const keep')
    const [oldCode, newCode] = [...keep.querySelectorAll<HTMLElement>('td.diff-code')]
    await userEvent.hover(oldCode)
    expect(visible(keep).map(b => b.dataset.commentSide + ' ' + b.dataset.commentLine)).toEqual(['old 10'])
    await userEvent.click(visible(keep)[0])
    blur()
    await userEvent.hover(newCode)
    expect(visible(keep).map(b => b.dataset.commentSide + ' ' + b.dataset.commentLine)).toEqual(['new 20'])
    await userEvent.click(visible(keep)[0])
    blur()
    // A removed line beside an added one: the left [+] comments on the old line, the right on the new.
    const changed = rowOf(container, 'const gone')
    const [left, right] = [...changed.querySelectorAll<HTMLElement>('td.diff-code')]
    await userEvent.hover(left)
    expect(visible(changed)).toHaveLength(1)
    await userEvent.click(visible(changed)[0])
    blur()
    await userEvent.hover(right)
    expect(visible(changed)).toHaveLength(1)
    await userEvent.click(visible(changed)[0])
    expect(anchors.map(where)).toEqual(['old 10', 'new 20', 'old 11', 'new 21'])
    // A wrapped line: each side's [+] is level with that side's line number.
    const long = rowOf(container, 'longLine')
    expect(long.getBoundingClientRect().height).toBeGreaterThan(30)
    for (const side of ['old', 'new'] as const) {
      const plus = long.querySelector<HTMLElement>(`.diff-comment-add[data-comment-side="${side}"]`)!.getBoundingClientRect()
      const number = long.querySelector<HTMLElement>(`.diff-line-select[data-diff-side="${side}"]`)!.getBoundingClientRect()
      expect(Math.abs(plus.top - number.top)).toBeLessThan(3)
      expect(plus.left).toBeGreaterThanOrEqual(number.right - 0.5)
    }
  })

  it('keeps the [+] reachable by keyboard: focus shows it and Enter comments', async () => {
    await page.viewport(1280, 800)
    const {anchors, getByRole} = mount()
    await userEvent.click(getByRole('button', {name: 'Side by side'}))
    const plus = getByRole('button', {name: 'Comment on old line 10 in f.ts'})
    plus.focus()
    expect(getComputedStyle(plus).opacity).toBe('1')
    await userEvent.keyboard('{Enter}')
    expect(anchors.map(where)).toEqual(['old 10'])
  })
})

describe('the [+] on a touch screen', () => {
  // Chromium here has a mouse, so the app's own (hover: none) rules are applied by hand: the test
  // copies every rule of those media blocks into a plain stylesheet and measures the result.
  function applyTouchRules(): () => void {
    const css: string[] = []
    for (const sheet of [...document.styleSheets]) {
      let rules: CSSRuleList
      try { rules = sheet.cssRules } catch { continue }
      for (const rule of [...rules]) {
        if (rule instanceof CSSMediaRule && rule.conditionText.replace(/\s/g, '') === '(hover:none)') {
          css.push(...[...rule.cssRules].map(r => r.cssText).filter(text => /diff-comment-add|diff-code-commentable/.test(text)))
        }
      }
    }
    const style = document.createElement('style')
    style.textContent = css.join('\n')
    document.head.append(style)
    return () => style.remove()
  }

  it('has a hit area of at least 24×24 px, though it shows 24×20', async () => {
    await page.viewport(390, 844)
    const undo = applyTouchRules()
    try {
      const {container, anchors} = mount()
      const plus = plusIn(rowOf(container, 'const fresh'))[0]
      expect(getComputedStyle(plus).opacity).toBe('1')
      const box = plus.getBoundingClientRect()
      expect(box.width).toBeGreaterThanOrEqual(24)
      expect(box.height).toBe(20)
      const x = box.left + box.width / 2
      // Rows at least 24 px apart, so no neighbor's [+] reaches into this one's area.
      expect(rowOf(container, 'const fresh').getBoundingClientRect().height).toBeGreaterThanOrEqual(24)
      // The full 24 px column, in its own row: from the button's top to 4 px below its visible bottom
      // (1 px in from each edge: hit-testing snaps fractional row edges to whole pixels).
      for (const y of [box.top + 1, box.top + 12, box.bottom + 3]) expect(document.elementFromPoint(x, y)?.closest('.diff-comment-add')).toBe(plus)
      expect(parseFloat(getComputedStyle(plus, '::after').height)).toBeGreaterThanOrEqual(24)
      await userEvent.click(plus)
      expect(anchors.map(where)).toEqual(['new 21'])
    } finally {
      undo()
    }
  })
})
