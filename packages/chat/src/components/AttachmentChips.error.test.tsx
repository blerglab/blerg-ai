import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ComposerChips } from './AttachmentChips'

describe('a file that could not be attached', () => {
  it('says why, not only that it failed', () => {
    render(<ComposerChips onRemove={() => {}} items={[{ key: 'k', name: 'shot.png', size: 10, status: 'error', error: 'this session’s uploaded files are full (500 MB)' }]} />)
    expect(screen.getByText(/Not attached: this session’s uploaded files are full \(500 MB\)/)).toBeInTheDocument()
  })
})
