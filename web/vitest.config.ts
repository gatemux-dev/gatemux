import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.test.{ts,tsx}'],
    // Node 22+ ships an experimental globalThis.localStorage stub that
    // shadows the DOM environment's real Storage; turn it off in workers.
    execArgv: ['--no-experimental-webstorage'],
  },
})
