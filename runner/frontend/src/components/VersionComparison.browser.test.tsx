import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ArtifactViewer, type ArtifactInfo, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const versions: ArtifactInfo[] = [1, 2, 3].map(version => ({ id: `v${version}`, version, name: 'roadmap.md', view: 'markdown', size: 100, content_type: 'text/markdown' }))
const files: TransportFiles = { list: async () => versions, raw: async (_s, id) => new Blob([`# Version ${id}\n${'Long line '.repeat(50)}\n${Array.from({ length: 200 }, (_, i) => `${id} line ${i}`).join('\n')}\n`]), download: async () => {}, remove: async () => {}, upload: async () => { throw Error('unused') } }
describe('published file comparison', () => {
  it.each([390, 3840])('compares and returns to the document at %ipx', async width => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    render(<ArtifactViewer sessionId="compare" files={files} artifact={versions[2]} onClose={() => {}} />)
    await screen.findByRole('heading', { name: 'Version v3' })
    fireEvent.click(await screen.findByRole('button', { name: 'Changes since v2' }))
    await waitFor(() => expect(document.querySelector('.diff-table-wrap')).not.toBeNull())
    expect(screen.queryByRole('heading', { name: 'Version v3' })).toBeNull()
    const main = document.querySelector<HTMLElement>('.diff-main')!
    expect(main.scrollHeight).toBeGreaterThan(main.clientHeight)
    main.scrollTop = 150
    expect(main.scrollTop).toBeGreaterThan(0)
    for (const control of document.querySelectorAll('.artifact-comparison-controls button, .artifact-comparison-controls select')) {
      const box = control.getBoundingClientRect()
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.right).toBeLessThanOrEqual(width)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(screen.getByLabelText('Compare to')).toHaveValue('v3')
    expect(screen.queryByRole('button', { name: 'Raw' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
    fireEvent.change(screen.getByLabelText('Compare from'), { target: { value: 'v1' } })
    fireEvent.change(screen.getByLabelText('Compare to'), { target: { value: 'v2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Document' }))
    expect(await screen.findByRole('heading', { name: 'Version v3' })).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: 'Changes since v2' }))
    expect(screen.getByLabelText('Compare from')).toHaveValue('v2')
    expect(screen.getByLabelText('Compare to')).toHaveValue('v3')
  })
})
