import { vi } from 'vitest'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn() }))
import { page } from 'vitest/browser'
import { clusterCodexConnectionTests } from './ClusterCodexConnection.cases'
import '../index.css'
clusterCodexConnectionTests(width => page.viewport(width, width === 390 ? 844 : 2160))
