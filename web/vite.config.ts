import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-plugin/vite'

/**
 * `clawflow web` serves the dashboard at http://HOST:PORT/ with an
 * SPA-fallback that returns index.html for any unknown path. That makes
 * absolute asset references (`base: '/'`) the right choice — a relative
 * `./assets/...` reference in index.html breaks at nested routes like
 * /runs/<slug>/<issue>/<ts> because the browser resolves it against the
 * current URL path instead of the server root.
 */
export default defineConfig({
  base: '/',
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
