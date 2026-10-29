import 'vitest/browser'
declare module 'vitest/browser' {
  interface BrowserCommands {
    setUsageReducedMotion: (reduce: boolean) => Promise<void>
  }
}
