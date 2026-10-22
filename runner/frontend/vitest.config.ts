import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { playwright } from '@vitest/browser-playwright'
import type { Plugin } from 'vite'
import { chatAliases } from './vite.config'

// A request recorder for the browser tests that prove a diagram makes no request: anything under
// /__beacon/ is recorded (and answered 404), and /__beacon_hits lists what was recorded.
function beacon(): Plugin {
  const hits: string[] = []
  return {
    name: 'test-beacon',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url?.startsWith('/__beacon/')) {
          hits.push(req.url)
          res.statusCode = 404
          res.end()
        } else if (req.url === '/__beacon_hits') {
          res.setHeader('content-type', 'application/json')
          res.setHeader('cache-control', 'no-store')
          res.end(JSON.stringify(hits))
        } else next()
      })
    },
  }
}

// Two test projects:
//   • unit    — fast, jsdom, fake timers. Pure logic + DOM-structure tests.
//   • browser — real Chromium (Playwright). The ONLY layer that can test
//               scrolling, because scroll depends on real layout
//               (scrollHeight / clientHeight / scrollTop) which jsdom does not
//               implement. Scroll/touch/device-switch regressions kept slipping
//               through precisely because they were never under a real browser.
// Browser tests are named *.browser.test.tsx and excluded from the unit project.
const prebundled = [
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
  'mermaid',
  'vega',
  'vega-lite',
  'vega-interpreter',
]

export default defineConfig({
  resolve: { alias: chatAliases },
  // Pre-bundle these so the browser project doesn't trigger a mid-run Vite
  // re-optimize (which aborts the first test with "Failed to fetch dynamically
  // imported module").
  optimizeDeps: { include: prebundled },
  test: {
    projects: [
      {
        plugins: [react()],
        resolve: { alias: chatAliases },
        test: {
          name: 'unit',
          environment: 'jsdom',
          testTimeout: 30000,
          hookTimeout: 30000,
          setupFiles: ['./src/test/setup.ts'],
          include: ['src/**/*.test.{ts,tsx}'],
          exclude: ['src/**/*.browser.test.{ts,tsx}', 'node_modules/**'],
          fakeTimers: {
            toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'setTimeout', 'clearTimeout'],
          },
        },
      },
      {
        plugins: [react(), beacon()],
        resolve: { alias: chatAliases },
        // An inline project does not inherit the root's optimizeDeps: without its own list, a cold
        // cache found react-dom/client partway through the run and reloaded the page.
        optimizeDeps: { include: prebundled },
        test: {
          name: 'browser',
          // Only this app's files: the Board's browser tests have their own project in board/web,
          // because files outside this root are not pre-scanned and a cold cache reloaded mid-run.
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
