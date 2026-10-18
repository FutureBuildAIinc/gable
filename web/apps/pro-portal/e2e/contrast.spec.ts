// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { collectContrast } from './contrast.ts'
import { installPortalMocks, setTheme } from './fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'

for (const theme of ['dark', 'light'] as const) {
  for (const surface of ['frame', 'sign-in'] as const) {
    test(`contrast ${surface} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 })
      await setTheme(page, theme)
      await installPortalMocks(page, { signedIn: surface === 'frame' })
      await page.goto('/today')
      await expect(page.getByTestId(surface === 'frame' ? 'today-screen' : 'sign-in-screen')).toBeVisible()
      const samples = await collectContrast(page, surface, theme)
      writeFileSync(join(shotDir, `contrast-${surface}-${theme}.json`), JSON.stringify(samples, null, 2))
      const failures = samples.filter((sample) => sample.ratio < (sample.kind === 'text' ? 4.5 : 3))
      expect(failures, JSON.stringify(failures.slice(0, 20), null, 2)).toEqual([])
    })
  }
}
