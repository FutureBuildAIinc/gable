// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '../../../e2e/font-guard'
import { join } from 'node:path'
import { installPortalMocks, setTheme } from './fixtures.ts'
import { testIds } from '../src/test/fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'
const widths = [390, 1024, 1440] as const
const themes = ['dark', 'light'] as const

for (const theme of themes) {
  for (const width of widths) {
    test(`Order ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/order')
      await expect(page.getByTestId('order-catalog-view')).toBeVisible()
      await expect(page.getByTestId('cart-subtotal')).toHaveText('$220.80')
      await page.screenshot({ path: join(shotDir, `order-${theme}-${width}.png`), fullPage: true })
    })

    test(`Pay ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/pay')
      await expect(page.getByTestId('pay-balance-tiles')).toBeVisible()
      await expect(page.getByTestId('tile-current-balance')).toHaveText('$4,125.75')
      await page.screenshot({ path: join(shotDir, `pay-${theme}-${width}.png`), fullPage: true })
    })
  }
}

test('Order screen supports reorder price-diff preview, material lists, and quote intake', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto('/order')
  await expect(page.getByTestId('order-catalog-view')).toBeVisible()

  await page.getByTestId('order-tab-reorder').click()
  await page.getByTestId(`reorder-preview-btn-${testIds.order}`).click()
  await expect(page.getByTestId('reorder-preview-card')).toBeVisible()
  await expect(page.getByTestId('reorder-original-total')).toHaveText('$234.24')
  await expect(page.getByTestId('reorder-current-total')).toHaveText('$247.20')

  await page.getByTestId('order-tab-lists').click()
  await expect(page.getByTestId('active-material-list')).toContainText('Standard Cedar Deck Package')

  await page.getByTestId('order-tab-requests').click()
  await page.getByTestId('intake-textarea').fill('24 pcs 2x4x8 SPF')
  await page.getByTestId('intake-parse-btn').click()
  await expect(page.getByTestId('intake-parsed-list')).toContainText('Matched LUM-248')
})

test('Pay screen selects open invoices and submits tokenized single-flight payment', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto('/pay')
  await expect(page.getByTestId('pay-balance-tiles')).toBeVisible()

  await page.getByTestId('select-all-open-btn').click()
  await expect(page.getByTestId('pay-selected-total')).toHaveText('$4,125.75')

  await page.getByTestId('submit-payment-btn').click()
  await expect(page.locator('.pro-success')).toContainText('PAY-000042')
})
