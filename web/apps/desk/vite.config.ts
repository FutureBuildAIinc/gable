// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig } from 'vite'

// https://vite.dev/config/
//
// base '/app/' places this bundle under the /app/ path in the combined
// nginx layout: the front door owns / and the desk owns /app/, so the two
// bundles' SPA fallbacks never collide. Assets land under /app/assets/ so
// the nginx location /app/assets/ serves them correctly.
export default defineConfig({
  base: '/app/',
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
