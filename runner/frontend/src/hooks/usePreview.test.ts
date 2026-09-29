import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import * as wsMock from '../test/wsMock'
import { usePreview } from './usePreview'

vi.mock('../ws', () => import('../test/wsMock'))

describe('usePreview', () => {
  beforeEach(() => wsMock.resetWsMock())

  it('starts empty', () => {
    const { result } = renderHook(() => usePreview())
    expect(result.current.html).toBe('')
  })

  it('updates html when a preview_updated message arrives', () => {
    const { result } = renderHook(() => usePreview())
    act(() => {
      wsMock.emit({ type: 'preview_updated', html: '<h1>Preview</h1>' })
    })
    expect(result.current.html).toBe('<h1>Preview</h1>')
  })
})
