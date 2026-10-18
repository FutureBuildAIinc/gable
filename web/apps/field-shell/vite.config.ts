// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig } from 'vite'

// The field shell is served under /field/ in web/nginx.conf (which redirects
// bare /field to /field/) and is also embedded at the root of the Tauri
// gable-field bundle (frontendDist: ../../web/apps/field-shell/dist). A
// relative base ('./') resolves assets under both /field/ and the Tauri
// bundle root.
export default defineConfig({
  base: './',
  build: {
    sourcemap: false,
    target: 'es2022',
  },
})
