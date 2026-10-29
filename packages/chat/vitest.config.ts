import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  // Keep test caches in this checkout when node_modules is a shared symlink.
  cacheDir: '../../.cache/vite-chat',
  test: {
    environment: 'jsdom',
    testTimeout: 30000,
    hookTimeout: 30000,
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    exclude: ['node_modules/**', 'dist/**'],
  },
})
