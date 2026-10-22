/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { playwright } from "@vitest/browser-playwright";

// The chat comes from the workspace package @blerglab/chat, built from its source here (dev,
// tests and the production bundle alike), as the runner's web app does.
const chat = (p: string) => fileURLToPath(new URL(`../../packages/chat/src/${p}`, import.meta.url));

export default defineConfig({
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
            provider: playwright(),
            headless: true,
            instances: [{ browser: "chromium" }],
          },
        },
      },
    ],
  },
});
