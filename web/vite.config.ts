import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-plugin/vite'

/**
 * The built-in CLI serves the dashboard from arbitrary roots
 * (http://127.0.0.1:PORT/) and the bundle's data fetches use relative
 * URLs ("./data/repos.json"), so `base: './'` keeps the absolute-URL
 * asset references in index.html working when the bundle is loaded
 * from a path other than `/`.
 */
export default defineConfig({
  base: './',
  plugins: [
    TanStackRouterVite(),
    react(),
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
  },
})
