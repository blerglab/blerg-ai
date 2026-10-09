import { describe, expect, it } from 'vitest'
import { attachmentNote, composeMessage, splitAttachmentNote } from './attachments'

const NOTE = 'Attached files (fetch them with: blerg-runner fetch --all): '

describe('attachment note', () => {
  it('lists each file with its size', () => {
    expect(attachmentNote([{ name: 'a.pdf', size: 1258291 }, { name: 'b.pdf', size: 800 * 1024 }]))
      .toBe(`${NOTE}a.pdf (1.2 MB), b.pdf (800 KB)`)
  })

  it('keeps a name on one line', () => {
    expect(attachmentNote([{ name: 'a\nb.pdf', size: 5 }])).toBe(`${NOTE}a b.pdf (5 B)`)
  })

  it('composes text, a blank line and the note; files alone get the generic line', () => {
    const files = [{ name: 'a.pdf', size: 10 }]
    expect(composeMessage('look', files)).toBe(`look\n\n${NOTE}a.pdf (10 B)`)
    expect(composeMessage('', files)).toBe(`Please take a look at the attached files.\n\n${NOTE}a.pdf (10 B)`)
    expect(composeMessage('look', [])).toBe('look')
  })

  it('splits a composed message back into text and files', () => {
    const msg = composeMessage('look', [{ name: 'a.pdf', size: 1258291 }, { name: 'my (draft).pdf', size: 800 * 1024 }])
    expect(splitAttachmentNote(msg)).toEqual({
      body: 'look',
      files: [{ name: 'a.pdf', size: '1.2 MB' }, { name: 'my (draft).pdf', size: '800 KB' }],
    })
    expect(splitAttachmentNote(`${NOTE}a.pdf (10 B)`).body).toBe('')
  })

  it('leaves anything that is not exactly a trailing note alone', () => {
    for (const t of ['hello', `${NOTE}`, `x ${NOTE}a.pdf (10 B)`, `${NOTE}a.pdf (10 B)\nmore`, `${NOTE}not a list`]) {
      expect(splitAttachmentNote(t)).toEqual({ body: t, files: [] })
    }
  })
})
