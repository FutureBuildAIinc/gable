// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { collectContrast } from './contrast.ts'
import { installPortalMocks, setTheme } from './fixtures.ts'
import { testIds } from '../src/test/fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'

const surfaces = [
  { key: 'frame', path: '/today', ready: 'today-screen', signedIn: true },
  { key: 'sign-in', path: '/today', ready: 'sign-in-screen', signedIn: false },
  { key: 'today', path: '/today', ready: 'today-screen', signedIn: true },
  { key: 'schedule', path: '/schedule', ready: 'schedule-screen', signedIn: true },
  { key: 'timeline', path: `/schedule/orders/${testIds.order}`, ready: 'order-timeline', signedIn: true },
] as const

for (const theme of ['dark', 'light'] as const) {
  for (const surface of surfaces) {
    test(`contrast ${surface.key} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 })
      await setTheme(page, theme)
      await installPortalMocks(page, { signedIn: surface.signedIn })
      await page.goto(surface.path)
      await expect(page.getByTestId(surface.ready)).toBeVisible()
      const samples = await collectContrast(page, surface.key, theme)
      writeFileSync(join(shotDir, `contrast-${surface.key}-${theme}.json`), JSON.stringify(samples, null, 2))
      const failures = samples.filter((sample) => sample.ratio < (sample.kind === 'text' ? 4.5 : 3))
      expect(failures, JSON.stringify(failures.slice(0, 20), null, 2)).toEqual([])
    })
  }
}
