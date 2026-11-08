import 'vitest/browser'
declare module 'vitest/browser' {
  interface BrowserCommands {
    openLockHolderPage: () => Promise<void>
    releaseLockHolderPage: () => Promise<void>
  }
}
