// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The screenshots run for the work shell: the built shell and desk served on
// one origin by scripts/preview-server.mjs (started and stopped by the
// webServer setting inside this one foreground run), the API mocked by
// page.route in the spec. Port 18394 is this item's preview port; the run
// never touches another port.

import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './screens',
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: [['list']],
  outputDir: 'screens/test-results',

  use: {
    baseURL: 'http://127.0.0.1:18394',
    trace: 'off',
    screenshot: 'off',
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
          ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } }
          : {}),
      },
    },
  ],

  webServer: {
    command: 'node scripts/preview-server.mjs',
    url: 'http://127.0.0.1:18394/shell/',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
