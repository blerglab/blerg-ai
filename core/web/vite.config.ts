/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  cacheDir: '.cache/vite',
  plugins: [react()],
  // Shared first-party UI must use this app’s React instance (core has its own install).
  resolve: { dedupe: ["react", "react-dom"] },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/auth": "http://localhost:8080",
      "/brand": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
    testTimeout: 30000,
    hookTimeout: 30000,
    setupFiles: ["./src/test/setup.ts"],
  },
});
