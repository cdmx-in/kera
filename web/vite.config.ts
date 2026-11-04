import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

const api = process.env.KERA_DEV_API ?? 'http://127.0.0.1:8080'

// With VITE_MOCK=1 the app fakes /api via fetch (src/api/mock.ts). <img> and
// <iframe> requests bypass fetch, so this dev middleware answers those few.
function mockAssets(): Plugin {
  const svg = (label: string, hue: number) =>
    `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64"><rect width="64" height="64" rx="12" fill="hsl(${hue} 55% 45%)"/><text x="32" y="42" font-family="sans-serif" font-size="30" text-anchor="middle" fill="#fff">${label}</text></svg>`
  return {
    name: 'kera-mock-assets',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = new URL(req.url ?? '/', 'http://x')
        const send = (type: string, body: string) => {
          res.setHeader('Content-Type', type)
          res.end(body)
        }
        if (url.pathname === '/api/v1/favicon') {
          const d = url.searchParams.get('domain') ?? '?'
          return send('image/svg+xml', svg(d[0].toUpperCase(), [...d].reduce((a, c) => a + c.charCodeAt(0), 0) % 360))
        }
        const card = url.pathname.match(/^\/__mock\/card\/(\d)\.svg$/)
        if (card) return send('image/svg+xml', svg(['A', 'B', 'C'][Number(card[1]) - 1] ?? '?', Number(card[1]) * 70))
        if (/^\/api\/v1\/files\/[^/]+\/content$/.test(url.pathname)) return send('text/csv', 'Model,Battery,Price\nA,14,1399\nB,11,1199\nC,9,999\n')
        next()
      })
    },
  }
}

// The Go binary embeds dist/. During `pnpm dev`, API calls go to a running `kera serve`.
export default defineConfig({
  plugins: [react(), tailwindcss(), ...(process.env.VITE_MOCK === '1' ? [mockAssets()] : [])],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  // The entry chunk is ~720 kB raw / ~225 kB gzip (budget: 600 kB gzip), so the raw-size warning is raised.
  build: { outDir: 'dist', emptyOutDir: false, assetsDir: 'assets', sourcemap: false, target: 'es2022', chunkSizeWarningLimit: 900 },
  server: {
    proxy: {
      '/api': { target: api, changeOrigin: true },
      '/oauth': { target: api, changeOrigin: true },
      '/ready': { target: api, changeOrigin: true },
    },
  },
})
