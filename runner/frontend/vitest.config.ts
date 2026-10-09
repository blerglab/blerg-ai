import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { playwright } from '@vitest/browser-playwright'
import { chatAliases } from './vite.config'

// Two test projects:
//   • unit    — fast, jsdom, fake timers. Pure logic + DOM-structure tests.
//   • browser — real Chromium (Playwright). The ONLY layer that can test
//               scrolling, because scroll depends on real layout
//               (scrollHeight / clientHeight / scrollTop) which jsdom does not
//               implement. Scroll/touch/device-switch regressions kept slipping
//               through precisely because they were never under a real browser.
// Browser tests are named *.browser.test.tsx and excluded from the unit project.
export default defineConfig({
  resolve: { alias: chatAliases },
  // Pre-bundle these so the browser project doesn't trigger a mid-run Vite
  // re-optimize (which aborts the first test with "Failed to fetch dynamically
  // imported module").
  optimizeDeps: {
    include: [
      'react',
      'react-dom',
      'react-dom/client',
      'react/jsx-dev-runtime',
      'react/jsx-runtime',
      '@testing-library/react',
      '@xterm/xterm',
      '@testing-library/user-event',
      'react-router-dom',
      'zustand',
      'react-markdown',
      'remark-gfm',
      'pdfjs-dist',
    ],
  },
  test: {
    projects: [
      {
        plugins: [react()],
        resolve: { alias: chatAliases },
        test: {
          name: 'unit',
          environment: 'jsdom',
          setupFiles: ['./src/test/setup.ts'],
          include: ['src/**/*.test.{ts,tsx}'],
          exclude: ['src/**/*.browser.test.{ts,tsx}', 'node_modules/**'],
          fakeTimers: {
            toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'setTimeout', 'clearTimeout'],
          },
        },
      },
      {
        plugins: [react()],
        resolve: { alias: chatAliases },
        test: {
          name: 'browser',
          include: ['src/**/*.browser.test.{ts,tsx}'],
          // cleanup() after each test so mounted terminals don't accumulate across
          // tests (they share one browser page and would otherwise leak).
          setupFiles: ['./src/test/browserSetup.ts'],
          // No fake timers: scrolling needs real requestAnimationFrame + real layout.
          browser: {
            enabled: true,
            provider: playwright(),
            headless: true,
            instances: [{ browser: 'chromium' }],
          },
        },
      },
    ],
  },
})
