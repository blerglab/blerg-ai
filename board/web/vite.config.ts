/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // test/run.mjs (test:scroll) is a separate, non-Vitest CDP harness —
    // exclude its fixtures/output so `vitest run` doesn't try to collect them.
    exclude: ["node_modules/**", "test/**"],
  },
});
