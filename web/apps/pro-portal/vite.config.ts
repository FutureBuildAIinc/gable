// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { resolve } from 'node:path'
import { defineConfig, type Plugin } from 'vite'

function publicQuoteRoute(): Plugin {
  const rewrite = (req: { url?: string }, _res: unknown, next: () => void) => {
    const entry = req.url?.startsWith('/q/') ? '/customer.html' : req.url?.startsWith('/dq/') ? '/dealer.html' : null
    if (entry !== null && req.url !== undefined) {
      const query = req.url.indexOf('?')
      req.url = `${entry}${query >= 0 ? req.url.slice(query) : ''}`
    }
    next()
  }
  return {
    name: 'gable-public-quote-route',
    configureServer(server) {
      server.middlewares.use(rewrite)
    },
    configurePreviewServer(server) {
      server.middlewares.use(rewrite)
    },
  }
}

export default defineConfig({
  plugins: [publicQuoteRoute()],
  build: {
    sourcemap: false,
    target: 'es2022',
    assetsDir: 'pro',
    rollupOptions: {
      input: {
        main: resolve(__dirname, 'index.html'),
        customer: resolve(__dirname, 'customer.html'),
        dealer: resolve(__dirname, 'dealer.html'),
      },
    },
  },
  server: {
    host: '127.0.0.1',
    port: 18396,
    strictPort: true,
    watch: {
      usePolling: true,
      interval: 1000,
      ignored: ['**/node_modules/**', '**/dist/**', '**/coverage/**', '**/.git/**'],
    },
    proxy: {
      '/api': {
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
  preview: {
    host: '127.0.0.1',
    port: 18396,
    strictPort: true,
  },
})
