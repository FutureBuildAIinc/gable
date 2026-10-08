// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig } from 'vite'

// https://vite.dev/config/
//
// assetsDir 'door' keeps this bundle's hashed files under /door/ in the
// combined nginx layout: the door is served at exactly / while the desk
// owns every other path, and the two bundles' asset URLs never collide.
export default defineConfig({
  build: {
    sourcemap: false,
    target: 'es2022',
    assetsDir: 'door',
  },
  server: {
    // Same dev proxy as the desk: the backend mounts every surface under
    // /api, and its probes live at the root.
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
  }
})
