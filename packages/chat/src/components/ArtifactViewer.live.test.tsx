import {useEffect} from 'react'
// BLG-470: a live file's tab has no version controls, redraws in place when its content changes
// (same id, later updated_at, no version bump), and offers Snapshot instead of Follow/Pin.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'
import { ChatContext, type ChatContextValue } from './context'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'

vi.mock('./VisualBlock', () => ({default: function MockVisualBlock({source, onRendered}: {source: string; onRendered?: (visual: unknown, source: string) => void}) {
 useEffect(() => {onRendered?.({svg:'<svg/>',width:640,height:360,dispose:()=>{}},source)}, [source,onRendered])
 return <div data-testid="chart-source">{source}</div>
}}))

let files: FakeFiles

function art(over: Partial<ArtifactInfo>): ArtifactInfo {
  return { id: 'live1', name: 'chart.png', size: 100, content_type: 'text/plain', view: 'text', live: true, version: 1, updated_at: '2026-11-03T00:00:00Z', ...over }
}

function chatValue(): ChatContextValue {
  return {
    sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null),
  }
}

function showLive(a: ArtifactInfo, latest?: ArtifactInfo) {
  const chat = chatValue()
  const draw = (artifact: ArtifactInfo, lat?: ArtifactInfo) => (
    <ChatContext.Provider value={chat}>
      <ArtifactViewer sessionId="s1" files={files} artifact={artifact} latest={lat} embedded onClose={vi.fn()} />
    </ChatContext.Provider>
  )
  const view = render(draw(a, latest))
  return { chat, view, draw }
}

beforeEach(() => {
  files = createFakeFiles()
})

afterEach(() => vi.restoreAllMocks())

describe('ArtifactViewer: a live file (BLG-470)', () => {
  it('shows no version select or Follow/Pin control, and shows Live with a relative revision time and exact tooltip', async () => {
    files.serve('hello')
    showLive(art({}))
    await screen.findByText('hello')
    expect(screen.queryByLabelText('Version')).toBeNull()
    expect(screen.queryByRole('button', { name: /Follow latest|Pin version/ })).toBeNull()
    const time = screen.getByTestId('viewer-live-updated')
    expect(time).toHaveTextContent('Live · updated')
    expect(time.querySelector('time')).toHaveAttribute('dateTime', '2026-11-03T00:00:00.000Z')
    expect(time.querySelector('time')?.getAttribute('title')).toBeTruthy()
  })

  it('offers Snapshot, which saves the current content as a normal versioned file', async () => {
    files.serve('current chart bytes')
    showLive(art({}))
    await screen.findByText('current chart bytes')
    fireEvent.click(await screen.findByRole('button', { name: 'Snapshot' }))
    await waitFor(() => expect(files.snapshotCalls).toEqual(['live1']))
    expect(await screen.findByText(/Saved as/)).toBeInTheDocument()
  })

  it('redraws in place when the same id gets a later updated_at (no version bump)', async () => {
    files.serve('v1 content')
    const first = art({})
    const { view, draw } = showLive(first)
    await screen.findByText('v1 content')

    files.serve('v2 content, refreshed')
    const refreshed = { ...first, updated_at: '2026-11-03T00:05:00Z' }
    view.rerender(draw(first, refreshed))

    expect(await screen.findByText('v2 content, refreshed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /is available/ })).toBeNull()
  })
})

it('keeps following unsupported live revisions with truthful freshness', async () => {
 const spec = (n: number) => JSON.stringify({mark:'trail',data:{values:[{x:1,y:n}]},encoding:{x:{field:'x',type:'quantitative'},y:{field:'y',type:'quantitative'}}})
 files.serve(spec(2))
 const first=art({name:'unsupported.vl.json',view:'json',content_type:'application/json'})
 const {view,draw}=showLive(first)
 await waitFor(()=>expect(screen.getByTestId('viewer-live-updated')).toHaveTextContent('Live · updated'))
 expect(screen.getByTestId('viewer-live-updated')).not.toHaveTextContent(/Static|unsupported-live/)
 files.serve(spec(3))
 view.rerender(draw(first,{...first,updated_at:'2026-11-03T00:05:00Z'}))
 await waitFor(()=>expect(files.rawCalls).toHaveLength(2))
 await waitFor(()=>expect(screen.getByTestId('chart-source')).toHaveTextContent(spec(3)))
 await waitFor(()=>expect(screen.getByTestId('viewer-live-updated').querySelector('time')).toHaveAttribute('dateTime','2026-11-03T00:05:00.000Z'))
 const save=vi.spyOn(await import('../transport/files'),'saveBlob').mockImplementation(()=>{})
 fireEvent.click(screen.getByRole('button',{name:'Download source'}))
 expect(save).toHaveBeenCalledWith(expect.any(Blob),'unsupported.vl.json')
 expect(await (save.mock.calls[0][0] as Blob).text()).toBe(spec(3))
 expect(files.downloads).toHaveLength(0)
})

it('advances freshness when the latest revision has identical bytes', async () => {
 files.serve(JSON.stringify({mark:'trail',data:{values:[{x:1,y:2}]}}))
 const first=art({name:'same.vl.json',view:'json'})
 const {view,draw}=showLive(first)
 await screen.findByTestId('chart-source')
 view.rerender(draw(first,{...first,updated_at:'2026-11-03T00:06:00Z'}))
 await waitFor(()=>expect(screen.getByTestId('viewer-live-updated').querySelector('time')).toHaveAttribute('dateTime','2026-11-03T00:06:00.000Z'))
})

it('follows revisions supplied directly by the file list', async () => {
 files.serve(JSON.stringify({mark:'trail',data:{values:[{x:1,y:2}]}}))
 const first=art({name:'direct.vl.json',view:'json'})
 const {view,draw}=showLive(first)
 await screen.findByTestId('chart-source')
 files.serve(JSON.stringify({mark:'trail',data:{values:[{x:1,y:3}]}}))
 view.rerender(draw({...first,updated_at:'2026-11-03T00:07:00Z'}))
 await waitFor(()=>expect(screen.getByTestId('viewer-live-updated').querySelector('time')).toHaveAttribute('dateTime','2026-11-03T00:07:00.000Z'),{timeout:1000})
})
