/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  cacheDir: '.cache/vite',
  plugins: [react()],
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
