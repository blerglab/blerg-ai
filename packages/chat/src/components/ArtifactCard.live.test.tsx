// BLG-470: a live file's card shows no version controls and shows a quiet relative timestamp, and
// redraws its preview when the current row (from the session's list) carries newer bytes.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import ArtifactCard from './ArtifactCard'
import { ChatContext, type ChatContextValue } from './context'
import { clearPreviewCache } from '../model/artifactPreview'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'
import type { ArtifactPayload } from '../types'

let files: FakeFiles
let chat: ChatContextValue

function card(payload: ArtifactPayload) {
  return render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
}

beforeEach(() => {
  clearPreviewCache()
  files = createFakeFiles()
  chat = {
    sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null),
    liveArtifact: () => undefined,
  }
})

afterEach(() => vi.restoreAllMocks())

describe('ArtifactCard: a live file', () => {
  it('shows only a muted relative time in the normal time position', () => {
    const payload: ArtifactPayload = {
      id: 'live1', name: 'chart.png', size: 10, content_type: 'image/png', view: 'image',
      version: 1, latest_version: 1, live: true, updated_at: '2026-11-03T00:00:00Z',
    }
    card(payload)
    expect(screen.queryByTestId('artifact-card-version')).toBeNull()
    const time = screen.getByTestId('artifact-card-updated')
    expect(time).not.toHaveTextContent('Updated')
    expect(time).toHaveClass('card-time', 'card-time-end')
    expect(time).toBe(time.parentElement?.lastElementChild)
    expect(time.querySelector('time')).toHaveAttribute('dateTime', '2026-11-03T00:00:00.000Z')
    expect(time.querySelector('time')?.getAttribute('title')).toBeTruthy()
  })

  it('shows the version badge for an ordinary (non-live) file with earlier versions', () => {
    const payload: ArtifactPayload = { id: 'v2', name: 'report.md', size: 10, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 2 }
    card(payload)
    expect(screen.getByTestId('artifact-card-version').textContent).toBe('· v2')
    expect(screen.queryByTestId('artifact-card-updated')).toBeNull()
  })

  it('refreshes size and updated_at from the session list when a republish lands', () => {
    const original: ArtifactPayload = { id: 'live2', name: 'chart.png', size: 10, content_type: 'image/png', view: 'image', version: 1, live: true, updated_at: '2026-11-03T00:00:00Z' }
    const fresh: ArtifactInfo = { id: 'live2', name: 'chart.png', size: 999, content_type: 'image/png', view: 'image', version: 1, live: true, updated_at: '2026-11-03T00:05:00Z' }
    chat.liveArtifact = (id: string) => (id === 'live2' ? fresh : undefined)
    card(original)
    expect(screen.getByText(/· 999 B/)).toBeTruthy()
  })

  it('redraws its preview after an in-place update instead of keeping the first republish forever', async () => {
    // Same id throughout (a live file never gets a new one): only updated_at moves.
    const payload: ArtifactPayload = { id: 'live3', name: 'notes.txt', size: 2, content_type: 'text/plain', view: 'text', live: true, updated_at: '2026-11-03T00:00:00Z' }
    files.serve('v1 content')
    chat.liveArtifact = (id: string) => (id === 'live3' ? { ...payload, size: 11 } : undefined)
    const view = card(payload)
    await screen.findByText('v1 content')

    files.serve('v2 content, refreshed')
    chat.liveArtifact = (id: string) => (id === 'live3' ? { ...payload, size: 22, updated_at: '2026-11-03T00:05:00Z' } : undefined)
    view.rerender(<ChatContext.Provider value={{ ...chat }}><ArtifactCard payload={payload} /></ChatContext.Provider>)

    expect(await screen.findByText('v2 content, refreshed')).toBeInTheDocument()
    expect(screen.queryByText('v1 content')).toBeNull()
  })
})

it('keeps unsupported live previews following revisions with current freshness',async()=>{
 const spec=(y:number)=>JSON.stringify({mark:'trail',data:{values:[{x:1,y}]},encoding:{x:{field:'x',type:'quantitative'},y:{field:'y',type:'quantitative'}}})
 files.serve(spec(2))
 const payload:ArtifactPayload={id:'unsupported',name:'trail.vl.json',size:spec(2).length,content_type:'application/json',view:'json',live:true,updated_at:'2026-11-03T00:00:00Z'}
 const view=card(payload)
 await waitFor(()=>expect(screen.getByTestId('artifact-preview-diagram')).toHaveTextContent('Live · updated'))
 files.serve(spec(3))
 view.rerender(<ChatContext.Provider value={chat}><ArtifactCard payload={{...payload,updated_at:'2026-11-03T00:05:00Z'}} /></ChatContext.Provider>)
 await waitFor(()=>expect(files.rawCalls).toHaveLength(2))
 await waitFor(()=>expect(screen.getByTestId('artifact-preview-diagram').querySelector('time')).toHaveAttribute('dateTime','2026-11-03T00:05:00.000Z'))
 expect(screen.getByTestId('artifact-preview-diagram')).not.toHaveTextContent(/Static snapshot|unsupported-live/)
})
