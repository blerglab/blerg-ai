/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

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
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    exclude: ["node_modules/**"],
  },
});
