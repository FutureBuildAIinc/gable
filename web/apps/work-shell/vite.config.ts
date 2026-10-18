// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig } from 'vite'

// The work shell is served under /shell/ so the framed apps (the desk first)
// keep owning the root paths on the same origin: the framed protocol needs
// the frame and the host on one origin, and the desk's router speaks absolute
// root paths.
export default defineConfig({
  base: '/shell/',
  build: {
    sourcemap: false,
    target: 'es2022',
  },
})
