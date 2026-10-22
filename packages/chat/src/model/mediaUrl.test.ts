// An SVG is never drawn from a same-origin blob: URL (opened in a new tab it would run its script as
// the app): every viewer makes its media URL through mediaUrl, which gives an SVG a data: URL.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mediaUrl, releaseMediaUrl } from './artifactViews'

interface Fs {
  readFileSync(path: string, encoding: 'utf8'): string
  readdirSync(path: string, options: { withFileTypes: true }): Array<{ name: string; isDirectory(): boolean }>
}
const fsName = 'node:fs'
const fs = (await import(/* @vite-ignore */ fsName)) as Fs
const SRC = (expect.getState().testPath ?? '').replace(/\/model\/mediaUrl\.test\.ts$/, '')

function sources(dir: string): string[] {
  const out: string[] = []
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = `${dir}/${e.name}`
    if (e.isDirectory()) out.push(...sources(p))
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p)
  }
  return out
}

const realCreate = URL.createObjectURL
afterEach(() => { URL.createObjectURL = realCreate })

describe('mediaUrl', () => {
  it('draws an SVG from a data: URL, never a blob: one', async () => {
    URL.createObjectURL = vi.fn(() => 'blob:x')
    const url = await mediaUrl(new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>']), 'image', 'image/svg+xml; charset=utf-8')
    expect(url.startsWith('data:image/svg+xml;base64,')).toBe(true)
    expect(atob(url.split(',')[1])).toBe('<svg xmlns="http://www.w3.org/2000/svg"/>')
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
  it('makes a typed object URL for a raster image', async () => {
    let made: Blob | null = null
    URL.createObjectURL = vi.fn((b: Blob) => { made = b; return 'blob:png' })
    expect(await mediaUrl(new Blob(['png']), 'image', 'image/png')).toBe('blob:png')
    expect(made!.type).toBe('image/png')
  })
  it('frees only what it made', () => {
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    releaseMediaUrl('data:image/svg+xml;base64,AA==')
    releaseMediaUrl(null)
    expect(revoke).not.toHaveBeenCalled()
    releaseMediaUrl('blob:x')
    expect(revoke).toHaveBeenCalledWith('blob:x')
    revoke.mockRestore()
  })
  it('is the only place the package makes an object URL (besides saving a download)', () => {
    const offenders = sources(SRC)
      .filter(p => fs.readFileSync(p, 'utf8').includes('URL.createObjectURL('))
      .map(p => p.slice(SRC.length + 1))
      .sort()
    expect(offenders).toEqual(['model/artifactViews.ts', 'transport/files.ts'])
  })
})
