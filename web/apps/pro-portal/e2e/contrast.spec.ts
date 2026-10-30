// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '../../../e2e/font-guard'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { collectContrast } from './contrast.ts'
import { installPortalMocks, setTheme } from './fixtures.ts'
import { testIds } from '../src/test/fixtures.ts'
import { publicIds, publicToken } from '../src/test/public-fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'

const surfaces = [
  { key: 'frame', path: '/today', ready: 'today-screen', signedIn: true, publicState: 'open' },
  { key: 'sign-in', path: '/today', ready: 'sign-in-screen', signedIn: false, publicState: 'open' },
  { key: 'today', path: '/today', ready: 'today-screen', signedIn: true, publicState: 'open' },
  { key: 'order', path: '/order', ready: 'order-catalog-view', signedIn: true, publicState: 'open' },
  { key: 'schedule', path: '/schedule', ready: 'schedule-screen', signedIn: true, publicState: 'open' },
  { key: 'timeline', path: `/schedule/orders/${testIds.order}`, ready: 'order-timeline', signedIn: true, publicState: 'open' },
  { key: 'pay', path: '/pay', ready: 'pay-balance-tiles', signedIn: true, publicState: 'open' },
  { key: 'quotes', path: '/quotes', ready: 'quotes-screen', signedIn: true, publicState: 'open' },
  { key: 'customer-selections', path: `/q/${publicToken}`, ready: 'public-page', signedIn: true, publicState: 'open' },
  { key: 'customer-code', path: `/q/${publicToken}`, ready: 'verification-code', signedIn: true, publicState: 'open' },
  { key: 'customer-signed', path: `/q/${publicToken}`, ready: 'public-signed', signedIn: true, publicState: 'signed' },
] as const

for (const theme of ['dark', 'light'] as const) {
  for (const surface of surfaces) {
    test(`contrast ${surface.key} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 })
      await setTheme(page, theme)
      await installPortalMocks(page, { signedIn: surface.signedIn, publicState: surface.publicState })
      await page.goto(surface.path)
      if (surface.key === 'customer-code') {
        await page.getByTestId(`approve-${publicIds.lineB}`).click()
        await page.getByTestId(`approve-${publicIds.lineC}`).click()
        await page.locator('input[name="full_name"]').fill('Customer Signoff')
        await page.locator('input[type="email"]').fill('customer@example.com')
        await page.getByTestId('public-accept').click()
      }
      await expect(page.getByTestId(surface.ready)).toBeVisible()
      const samples = await collectContrast(page, surface.key, theme)
      writeFileSync(join(shotDir, `contrast-${surface.key}-${theme}.json`), JSON.stringify(samples, null, 2))
      const failures = samples.filter((sample) => sample.ratio < (sample.kind === 'text' ? 4.5 : 3))
      expect(failures, JSON.stringify(failures.slice(0, 20), null, 2)).toEqual([])
    })
  }
}
