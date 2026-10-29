import { defineConfig, defineProject } from 'vitest/config'
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

// Three test projects:
//   • unit    — fast, jsdom, fake timers. Pure logic + DOM-structure tests.
//   • browser — real browsers (Playwright). The ONLY layer that can test
//               scrolling, because scroll depends on real layout
//               (scrollHeight / clientHeight / scrollTop) which jsdom does not
//               implement. Scroll/touch/device-switch regressions kept slipping
//               through precisely because they were never under a real browser.
//   • browser-performance — strict timing budgets, serial files after all other projects.
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
  'highlight.js/lib/languages/dockerfile',
  'highlight.js/lib/languages/makefile',
]

function browserProject(performance: boolean) {
  return defineProject({
    plugins: [react(), beacon()],
    cacheDir: performance ? '../../.cache/vite-runner-performance' : '../../.cache/vite-runner-browser',
    resolve: { alias: chatAliases },
    // An inline project does not inherit the root's optimizeDeps: without its own list, a cold
    // cache found react-dom/client partway through the run and reloaded the page.
    optimizeDeps: { include: prebundled },
    test: {
      name: performance ? 'browser-performance' : 'browser',
      // Bound simultaneous pages so the corpus and interactive tests share one browser
      // without starving pointer/animation work on machines with many reported CPUs.
      maxWorkers: performance ? 1 : 4,
      // A strict <16 ms sample must not compete with other test files or projects.
      fileParallelism: !performance,
      sequence: { groupOrder: performance ? 2 : 1 },
      // Only this app's files: the Board's browser tests have their own project in board/web,
      // because files outside this root are not pre-scanned and a cold cache reloaded mid-run.
      include: performance ? ['src/**/*.performance.browser.test.{ts,tsx}'] : ['src/**/*.browser.test.{ts,tsx}'],
      exclude: performance ? [] : ['src/**/*.performance.browser.test.{ts,tsx}'],
      // cleanup() after each test so mounted terminals don't accumulate across
      // tests (they share one browser page and would otherwise leak).
      setupFiles: ['./src/test/browserSetup.ts'],
      // No fake timers: scrolling needs real requestAnimationFrame + real layout.
      browser: {
        enabled: true,
        commands: {
          async setUsageReducedMotion({ page }, reduce: boolean) {
            await page.emulateMedia({ reducedMotion: reduce ? 'reduce' : 'no-preference' })
          },
        },
        // The test iframe scales to fit Playwright's outer viewport. Owner
        // captures need enough room to retain the requested pixel dimensions.
        // Every browser runs in UTC, the timezone CI's runners use, so date rendering (Mermaid Gantt
        // axes, formatted times) is the same on every machine.
        provider: playwright((process.env.VITE_IMAGE_ZOOM_SHOTS_DIR || process.env.VITE_LOADER_SHOTS_DIR || process.env.VITE_LOADER_REDUCED_MOTION || process.env.VITE_TAB_FOCUS_SHOTS_DIR || process.env.VITE_FILE_TAB_SHOTS_DIR || process.env.VITE_LAUNCH_SHOTS_DIR || process.env.VITE_CHROME_SHOTS_DIR || process.env.VITE_SHELL_TAB_SHOTS_DIR) ? {
          contextOptions: { timezoneId: 'UTC', viewport: { width: 3840, height: 2160 }, ...(process.env.VITE_LOADER_REDUCED_MOTION ? { reducedMotion: 'reduce' as const } : {}) },
        } : { contextOptions: { timezoneId: 'UTC' } }),
        headless: true,
        instances: [
          { browser: 'chromium' },
          // Shortcut ownership must also hold in Firefox after normal session use.
          // Playwright's Firefox focus emulation does not cover Vitest's child iframe:
          // input in another page can leave activeElement set but suppress native Enter
          // activation. Keep Firefox files serial; Chromium still exercises four workers.
          // https://github.com/microsoft/playwright/issues/42933
          ...(!performance ? [{ browser: 'firefox' as const, fileParallelism: false, include: ['src/components/PaletteClaim.browser.test.tsx', 'src/components/CommandPalette.browser.test.tsx', 'src/components/PaletteOwnership.browser.test.tsx', 'src/components/ShellWorkspace.browser.test.tsx', 'src/components/SessionTabOverflow.browser.test.tsx'] }] : []),
        ],
      },
    },
  })
}

export default defineConfig({
  // Keep test caches in this checkout when node_modules is a shared symlink.
  cacheDir: '../../.cache/vite-runner',
  resolve: { alias: chatAliases },
  // Pre-bundle these so the browser project doesn't trigger a mid-run Vite
  // re-optimize (which aborts the first test with "Failed to fetch dynamically
  // imported module").
  optimizeDeps: { include: prebundled },
  test: {
    projects: [
      {
        cacheDir: '../../.cache/vite-runner-unit',
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
      browserProject(false),
      browserProject(true),
    ],
  },
})
