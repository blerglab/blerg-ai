import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { INLINE_IMAGE_MAX, clearPreviewCache, loadPreviewUrl, previewKind } from './artifactPreview'

const png = { id: 'a1', view: 'image', content_type: 'image/png' }

describe('previewKind', () => {
  it('draws a small image inline, tiles video and audio, and leaves the rest alone', () => {
    expect(previewKind('image', 1024)).toBe('image')
    expect(previewKind('image', INLINE_IMAGE_MAX + 1)).toBeNull()
    expect(previewKind('video', 500 * 1024 * 1024)).toBe('media')
    expect(previewKind('audio', 1)).toBe('media')
    expect(previewKind('pdf', 1)).toBeNull()
    expect(previewKind('none', 1)).toBeNull()
  })
})

describe('loadPreviewUrl', () => {
  let created: Blob[]
  beforeEach(() => {
    created = []
    Object.assign(URL, {
      createObjectURL: vi.fn((b: Blob) => { created.push(b); return `blob:p${created.length}` }),
      revokeObjectURL: vi.fn(),
    })
  })
  afterEach(() => {
    clearPreviewCache()
    vi.restoreAllMocks()
  })

  it('asks the transport for the bytes once per session and file, and types the blob by the view', async () => {
    const raw = vi.fn(async () => new Blob(['bytes'], { type: 'text/html' }))
    const a = await loadPreviewUrl('s1', png, raw)
    const b = await loadPreviewUrl('s1', png, raw)
    expect(a).toBe('blob:p1')
    expect(b).toBe(a)
    expect(raw).toHaveBeenCalledTimes(1)
    expect(raw).toHaveBeenCalledWith('a1')
    expect(created[0].type).toBe('image/png') // the server's word is not taken for the type
    await expect(loadPreviewUrl('s2', png, raw)).resolves.toBe('blob:p2')
  })

  it('forgets a failed fetch so a retry can work', async () => {
    const raw = vi.fn<(id: string) => Promise<Blob>>()
      .mockRejectedValueOnce(new Error('HTTP 500'))
      .mockResolvedValueOnce(new Blob(['ok']))
    await expect(loadPreviewUrl('s1', png, raw)).rejects.toThrow('HTTP 500')
    await expect(loadPreviewUrl('s1', png, raw)).resolves.toBe('blob:p1')
    expect(raw).toHaveBeenCalledTimes(2)
  })

  it('clearPreviewCache revokes every URL it handed out', async () => {
    await loadPreviewUrl('s1', png, async () => new Blob(['x']))
    clearPreviewCache()
    await Promise.resolve()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:p1')
  })
})
