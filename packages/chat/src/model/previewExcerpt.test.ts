import { describe, expect, it } from 'vitest'
import { excerptOf } from './previewExcerpt'

const lines = (n: number) => Array.from({ length: n }, (_, i) => `l${i + 1}`).join('\n')

describe('excerptOf', () => {
  it('takes the first 12 lines of text and says when there is more', () => {
    const e = excerptOf('text', 'a.txt', 'text/plain', lines(30))
    expect(e).toMatchObject({ kind: 'text', lang: null, more: true })
    expect(e.kind === 'text' && e.text.split('\n')).toHaveLength(12)
    expect(excerptOf('text', 'a.txt', 'text/plain', lines(12) + '\n')).toMatchObject({ more: false })
  })
  it('picks the language of a code file by its extension', () => {
    expect(excerptOf('text', 'main.go', 'text/plain', 'package main')).toMatchObject({ lang: 'go' })
  })
  it('pretty-prints JSON that parses, and leaves JSON that does not', () => {
    const e = excerptOf('json', 'a.json', 'application/json', '{"a":1,"b":[2]}')
    expect(e).toMatchObject({ kind: 'text', lang: 'json' })
    expect(e.kind === 'text' && e.text).toContain('\n  "a": 1')
    expect(excerptOf('json', 'a.json', '', '{oops')).toMatchObject({ text: '{oops' })
  })
  it('cuts a very long line', () => {
    const e = excerptOf('text', 'min.js', '', 'x'.repeat(5000))
    expect(e.kind === 'text' && e.text.length).toBeLessThan(300)
  })
  it('keeps the first 6 rows and 6 columns of a table, tab-separated for .tsv', () => {
    const csv = Array.from({ length: 10 }, (_, r) => Array.from({ length: 9 }, (_, c) => `r${r}c${c}`).join(',')).join('\n')
    const e = excerptOf('csv', 'a.csv', 'text/csv', csv)
    expect(e).toMatchObject({ kind: 'csv', more: true })
    expect(e.kind === 'csv' && [e.rows.length, e.rows[0].length]).toEqual([6, 6])
    const t = excerptOf('csv', 'a.tsv', '', 'a\tb\n1\t2')
    expect(t.kind === 'csv' && t.rows).toEqual([['a', 'b'], ['1', '2']])
  })
  it('marks markdown', () => {
    expect(excerptOf('markdown', 'r.md', '', '# hi\n\ntext')).toMatchObject({ kind: 'markdown', more: false })
  })
})
