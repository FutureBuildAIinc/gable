// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright end-to-end tests for the Gable web stack, run against the real
 * stack: web/nginx.conf serving the real built bundles on one origin, proxying
 * /api to `core serve` (AUTH_MODE=dev) over a migrated and seeded Postgres.
 *
 * web/scripts/e2e-stack.sh brings that stack up and down; the CI job runs the
 * same script. BASE_URL names the nginx origin. SHOTS_DIR is where the
 * screenshots land. The browser is Playwright's own default install
 * (`npx playwright install chromium`); no path is hardcoded here.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [['list']],
  outputDir: process.env.PW_OUTPUT_DIR ?? 'test-results',

  use: {
    baseURL: process.env.BASE_URL ?? 'http://127.0.0.1:18080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    viewport: { width: 1440, height: 900 },
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
        // Only when set: a machine whose cache holds a different Chromium revision points here.
        ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
          ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } }
          : {}),
      },
    },
  ],
});
