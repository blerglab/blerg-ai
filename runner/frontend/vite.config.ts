import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The chat surface comes from the workspace package @blerglab/chat. It is built from its source
// here (dev, tests and the production bundle alike), so the app never depends on the package's
// dist; the tarball the runner serves to outside apps is a separate build (packages/chat).
const chat = (p: string) => fileURLToPath(new URL(`../../packages/chat/src/${p}`, import.meta.url))

export const chatAliases = [
  { find: /^@blerglab\/chat$/, replacement: chat('index.ts') },
  { find: /^@blerglab\/chat\/tokens\.css$/, replacement: chat('theme/tokens.css') },
  { find: /^@blerglab\/chat\/blerg\.css$/, replacement: chat('theme/blerg.css') },
]

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: { alias: chatAliases },
})
