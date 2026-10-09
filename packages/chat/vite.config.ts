import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { copyFileSync, mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

// Library build: one ESM entry, React left to the host, the three stylesheets copied beside it
// (tokens.css is the theme contract, blerg.css the runner's theme, chat.css the components').
export default defineConfig({
  plugins: [
    react(),
    {
      name: 'copy-stylesheets',
      closeBundle() {
        mkdirSync(resolve(__dirname, 'dist'), { recursive: true })
        for (const f of ['tokens.css', 'blerg.css']) {
          copyFileSync(resolve(__dirname, 'src/theme', f), resolve(__dirname, 'dist', f))
        }
      },
    },
  ],
  build: {
    lib: {
      entry: resolve(__dirname, 'src/index.ts'),
      formats: ['es'],
      fileName: () => 'index.js',
      cssFileName: 'chat',
    },
    rollupOptions: {
      external: ['react', 'react-dom', 'react/jsx-runtime', 'react-dom/client'],
    },
    sourcemap: true,
  },
})
