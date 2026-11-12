import {expect, it, vi} from 'vitest'
import {blergTransport, invalidateChatTransport} from './chatTransport'
import * as ws from '../test/wsMock'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', () => ({getAccessToken: () => null, ensureFreshToken: async () => false}))
it('purges live dataset rows when account or desktop authority invalidates the transport', () => {
  invalidateChatTransport()
  ws.send.mockImplementation(() => true)
  const transport = blergTransport()
  const receive = vi.fn()
  const off = transport.files!.subscribeDataset!('session', {kind: 'dataset', name: 'blerg.workers', parameters: {}}, receive)
  invalidateChatTransport()
  expect(receive.mock.lastCall?.[0]).toMatchObject({status: 'access_removed', rows: []})
  off()
  ws.resetWsMock()
})
