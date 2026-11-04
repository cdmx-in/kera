import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The Go binary embeds dist/. During `pnpm dev`, API calls go to a running `kera serve`.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: { outDir: 'dist', emptyOutDir: false, assetsDir: 'assets', sourcemap: false, target: 'es2022' },
  server: {
    proxy: {
      '/api': { target: process.env.KERA_DEV_API ?? 'http://127.0.0.1:8080', changeOrigin: true },
      '/oauth': { target: process.env.KERA_DEV_API ?? 'http://127.0.0.1:8080', changeOrigin: true },
      '/ready': { target: process.env.KERA_DEV_API ?? 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
})
