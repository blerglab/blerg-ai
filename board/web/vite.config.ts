/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { playwright } from "@vitest/browser-playwright";
import type { Page } from "playwright";

// The chat comes from the workspace package @blerglab/chat, built from its source here (dev,
// tests and the production bundle alike), as the runner's web app does.
const chat = (p: string) => fileURLToPath(new URL(`../../packages/chat/src/${p}`, import.meta.url));

// BLG-487: a second real page on the dev server's own origin, used by SilentRefreshLock.browser.test.ts
// to hold the shared navigator.locks refresh lock the way another tab's lingering silent-refresh
// frame would, so the test can prove the caller's bound covers the lock wait too.
const lockHolderPages = new WeakMap<object, Page>();

export default defineConfig({
  cacheDir: '.cache/vite',
  plugins: [react()],
  resolve: {
    alias: [
      { find: /^@blerglab\/chat$/, replacement: chat("index.ts") },
      { find: /^@blerglab\/chat\/tokens\.css$/, replacement: chat("theme/tokens.css") },
      { find: /^@blerglab\/chat\/blerg\.css$/, replacement: chat("theme/blerg.css") },
    ],
  },
  // Keep license comments (/*! … */, @license) when minifying: Vite drops them by default, and the
  // bundle carries code adapted under licenses that ask for their notice (THIRD_PARTY_NOTICES.md).
  build: { rolldownOptions: { output: { comments: { legal: true } } } },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
  // Pre-bundle the browser tests' dependencies so a cold Vite cache never re-optimizes (and
  // reloads the page) partway through the run.
  optimizeDeps: {
    include: [
      "react",
      "react-dom",
      "react-dom/client",
      "react/jsx-dev-runtime",
      "react/jsx-runtime",
      "@testing-library/react",
      "react-markdown",
      "remark-gfm",
      "zustand",
    ],
  },
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: "unit",
          environment: "jsdom",
          testTimeout: 30000,
          hookTimeout: 30000,
          setupFiles: ["./src/test/setup.ts"],
          exclude: ["node_modules/**", "src/**/*.browser.test.{ts,tsx}"],
        },
      },
      {
        // Real Chromium (Playwright) for layout: the phone and large-screen passes (#50). The Board
        // has its own project, apart from the runner's, so neither's optimizer reloads the other.
        extends: true,
        test: {
          name: "browser",
          include: ["src/**/*.browser.test.{ts,tsx}"],
          testTimeout: 30000,
          hookTimeout: 30000,
          browser: {
            enabled: true,
            provider: playwright(process.env.VITE_CHROME_SHOTS_DIR ? { contextOptions: { viewport: { width: 3840, height: 2160 } } } : {}),
            headless: true,
            instances: [{ browser: "chromium" }],
            commands: {
              // BLG-487: open a second real page on this same origin and have it take the
              // shared refresh lock, the way another tab's lingering silent-refresh frame
              // would — so the test can prove the caller's bound covers the lock wait, not
              // just the frame's own wait.
              async openLockHolderPage({ page, context }) {
                const url = new URL("/src/test/silentRefreshLockHarness.html", page.url()).toString();
                const holder = await context.newPage();
                await holder.goto(url);
                await holder.evaluate(() => (window as unknown as { __lockHeld: Promise<void> }).__lockHeld);
                lockHolderPages.set(page, holder);
              },
              async releaseLockHolderPage({ page }) {
                const holder = lockHolderPages.get(page);
                if (!holder) return;
                await holder.evaluate(() => (window as unknown as { __releaseLock: () => void }).__releaseLock());
                await holder.close();
                lockHolderPages.delete(page);
              },
            },
          },
        },
      },
    ],
  },
});
