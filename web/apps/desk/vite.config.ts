// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { defineConfig, type Plugin } from 'vite'

// The `/pos` service worker (ADR 0011 section 5.2) is emitted with the build's
// own asset list, so its install precaches the whole bundle; the version is
// taken from that list, so every deploy installs a fresh worker and cache.
function posServiceWorker(): Plugin {
  return {
    name: 'gable-pos-sw',
    apply: 'build',
    generateBundle(_options, bundle) {
      const assets = Object.keys(bundle)
        .filter((name) => name.startsWith('assets/'))
        .sort()
        .map((name) => `/${name}`)
      const version = createHash('sha256').update(assets.join('\n')).digest('hex').slice(0, 16)
      const template = readFileSync(new URL('./sw/pos-sw.js', import.meta.url), 'utf8')
      if (!template.includes("'__POS_PRECACHE_VERSION__'") || !template.includes('__POS_PRECACHE_ASSETS__;')) {
        throw new Error('sw/pos-sw.js lost its precache placeholders')
      }
      this.emitFile({
        type: 'asset',
        fileName: 'pos-sw.js',
        source: template
          .replace("'__POS_PRECACHE_VERSION__'", JSON.stringify(version))
          .replace('__POS_PRECACHE_ASSETS__;', `${JSON.stringify(assets)};`),
      })
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [posServiceWorker()],
  build: {
    sourcemap: false,
    target: 'es2022',
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            // Lit runtime
            if (id.includes('/lit/') || id.includes('/@lit/') || id.includes('/lit-html/') || id.includes('/@lit/reactive-element/')) {
              return 'vendor-lit';
            }
            // Charts
            if (id.includes('/chart.js/')) {
              return 'vendor-chartjs';
            }
            // Icons
            if (id.includes('/lucide/')) {
              return 'vendor-icons';
            }
            // Maps
            if (id.includes('/leaflet/')) {
              return 'vendor-leaflet';
            }
          }
        },
      },
    },
  },
  server: {
    // Forward /api/* to the backend VERBATIM. The backend mounts every
    // surface under /api (ERP /api/v1, portal /api/portal/v1, partner,
    // integration, a2a) — the historical ROOT_MOUNTED prefix-stripping
    // rewrite here dated from before that unification and silently broke
    // dev for orders/customers/products/me once the backend moved them
    // under /api/v1 (prod was unaffected: no proxy there). Same-origin
    // semantics as App Platform's path routing.
    proxy: {
      '/api': {
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: false,
      },
      // The backend serves its probes at the root, not under /api. The deploy
      // spec (.do/app-*.yaml) routes /healthz to the backend with
      // preserve_path_prefix, so Tech Admin's System Health panel reads
      // same-origin in a real deployment; without this rule the SPA fallback
      // would answer it with index.html in dev.
      '/healthz': {
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
  preview: {
    proxy: {
      '/api': {
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: false,
      },
      '/healthz': {
        target: process.env.VITE_API_PROXY || 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
})
