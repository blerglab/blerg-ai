import { render } from '@testing-library/react'
import { expect, it } from 'vitest'
import { attachmentNote } from '../model/attachments'
import { ComposerChips, MessageText } from './AttachmentChips'

it('uses the filename detector for composer and message file chips', () => {
  const { container } = render(<>
    <ComposerChips items={[{ key: 'a', name: 'main.go', size: 12, status: 'done' }]} onRemove={() => {}} />
    <MessageText text={attachmentNote([{ name: 'notes.md', size: 12 }])} />
  </>)
  expect(container.querySelector('.composer-attachment .attach-chip-icon')).toHaveAttribute('data-file-icon', 'go')
  expect(container.querySelector('.in-message .file-icon')).toHaveAttribute('data-file-icon', 'markdown')
  expect(container.querySelector('.attach-chip-preview .file-icon')).toHaveAttribute('data-file-icon', 'go')
})
