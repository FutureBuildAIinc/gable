// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright end-to-end tests for the Gable web stack.
 *
 * Two bundles are served by the test server (test-server.js):
 *   http://localhost:5000/        → front-door (micro-app selector)
 *   http://localhost:5000/app/*  → desk (ERP workspace)
 *
 * The test server proxies /api/* to the Go backend (TEST_API_TARGET).
 * Screenshots are written to SHOTS_DIR/r1-8/ (default
 * /home/colton/Desktop/FBHQ/gable-v1/shots/r1-8/).
 *
 * Run with:
 *   BASE_URL=http://localhost:5000 TEST_API_TARGET=http://localhost:8083 \
 *     node test-server.js &
 *   npx playwright test
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [['list'], ['html', { open: 'never' }]],

  use: {
    baseURL: process.env.BASE_URL ?? 'http://localhost:5000',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    viewport: { width: 1440, height: 900 },
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // Use the cached Chromium binary rather than downloading a new headless shell.
        launchOptions: {
          executablePath:
            process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ??
            '/home/colton/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome',
        },
      },
    },
  ],
});
