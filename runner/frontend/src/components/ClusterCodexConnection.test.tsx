import { vi } from 'vitest'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn() }))
import { clusterCodexConnectionTests } from './ClusterCodexConnection.cases'
clusterCodexConnectionTests()
