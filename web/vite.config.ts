import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          // Framework code changes far less often than app code; keeping it
          // in its own chunk preserves the browser cache across deploys.
          vendor: ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: Object.fromEntries(
      ['/v1', '/admin', '/auth', '/me', '/invite', '/reset', '/docs', '/healthz', '/readyz'].map(
        (path) => [path, process.env.GATEMUX_PROXY_TARGET ?? 'http://localhost:4000'],
      ),
    ),
  },
})
