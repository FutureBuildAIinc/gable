// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig, configDefaults } from 'vitest/config'

export default defineConfig({
  test: {
    globals: true,
    environment: 'jsdom',
    // The screens spec is Playwright's, run by its own config; vitest never sees it.
    exclude: [...configDefaults.exclude, 'screens/**'],
    reporters: process.env.GITHUB_ACTIONS ? ['default', 'github-actions'] : ['default'],
  },
})
