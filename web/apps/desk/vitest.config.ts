// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    globals: true,
    environment: 'jsdom',
    css: true,
    setupFiles: ['./src/test/setup.ts'],
    // Readable locally; annotated inline on the PR diff when running in Actions.
    reporters: process.env.GITHUB_ACTIONS ? ['default', 'github-actions'] : ['default'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: './coverage',
      include: ['src/**/*.ts'],
      // Type-only modules and test scaffolding have no runtime behavior to cover.
      exclude: [
        'src/**/*.test.ts',
        'src/test/**',
        'src/types/**',
        'src/vite-env.d.ts',
      ],
      // Intentionally NO thresholds: the baseline is near zero, and a failing
      // gate here would block contributors instead of informing them. Report only.
    },
  },
})
