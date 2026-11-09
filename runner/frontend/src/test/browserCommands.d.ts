import 'vitest/browser'
declare module 'vitest/browser' {
  interface BrowserCommands {
    needsYouScreenshot: (width: number, theme: string, surface?: string) => Promise<void>
    testPerformanceScreenshot: (width: number, state: string) => Promise<void>
    contentExportScreenshot: (width: number, theme: string, print?: boolean) => Promise<void>
    layoutScreenshot: (width: number, theme: string, surface?: 'layouts' | 'layouts-saved' | 'workspace') => Promise<void>
    settingsScreenshot: (width: number, theme: string) => Promise<void>
    questionScreenshot: (width: number) => Promise<void>
    startChartCapture: (name: string, recording?: boolean) => Promise<void>
    stopChartCapture: (samples?: {timestamp: number; state: string; rect?: number[]; stage: string; surface?: number; hosted?: boolean}[]) => Promise<{count: number; truncated: boolean; terminal: boolean; analyzed: number; blankFrames: number[]}>
    chartScreenshot: (name: string, width: number, height: number) => Promise<string>
    chartFrames: (name: string, samples: unknown[]) => Promise<void>
    startDatasetFixture: (origin: string, mcp?: boolean) => Promise<{url: string; token: string; sessionId: string}>
    stopDatasetFixture: (token: string) => Promise<void>
    startBlackboardFixture: (origin:string) => Promise<{url:string;token:string;agentToken:string;sessionId:string;channelId:string}>
    stopBlackboardFixture: (token:string) => Promise<void>
    startSideChannelFixture: () => Promise<{url:string;token:string;sessionId:string;recommendation:import('../../../../packages/chat/src/transport/types').ChannelRecommendation}>
    stopSideChannelFixture: (token:string) => Promise<void>
    transcriptInput: (kind: string) => Promise<void>
    workspaceTaskTime: (action: 'start' | 'stop') => Promise<{ taskMs: number; scriptMs: number; layoutMs: number; styleMs: number }>
    workspaceTrace: (action: 'start' | 'stop') => Promise<string>
    workspaceKey: () => Promise<void>
    startWhatsNewFixture: () => Promise<{url:string;token:string}>
    stopWhatsNewFixture: (token:string) => Promise<void>
    setUsageReducedMotion: (reduce: boolean) => Promise<void>
    openLockHolderPage: () => Promise<void>
    releaseLockHolderPage: () => Promise<void>
  }
}
