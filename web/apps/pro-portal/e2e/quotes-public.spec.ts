// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test, type Page } from '../../../e2e/font-guard'
import { join } from 'node:path'
import { installPortalMocks, setTheme } from './fixtures.ts'
import { quoteIds } from '../src/test/customer-quote-fixtures.ts'
import { publicIds, publicToken } from '../src/test/public-fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'
const widths = [390, 1024, 1440] as const
const themes = ['dark', 'light'] as const

async function reachCodeStep(page: Page): Promise<void> {
  await page.getByTestId(`approve-${publicIds.lineB}`).click()
  await page.getByTestId(`approve-${publicIds.lineC}`).click()
  await page.locator('input[name="full_name"]').fill('Customer Signoff')
  await page.locator('input[type="email"]').fill('customer@example.com')
  await page.getByTestId('public-accept').click()
  await expect(page.getByTestId('verification-code')).toBeVisible()
}

for (const theme of themes) {
  for (const width of widths) {
    test(`quote builder ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/quotes')
      await expect(page.getByTestId('quote-lines')).toBeVisible()
      await expect(page.getByTestId('quote-summary')).toContainText('$526.96')
      await page.screenshot({ path: join(shotDir, `quotes-${theme}-${width}.png`), fullPage: true })
    })

    test(`customer selections ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto(`/q/${publicToken}`)
      await expect(page.getByTestId('public-selections')).toBeVisible()
      await expect(page.getByTestId('public-page')).not.toContainText('Secret internal labour')
      await expect(page.getByTestId('public-page')).not.toContainText('dealer_cost')
      await page.screenshot({ path: join(shotDir, `customer-selections-${theme}-${width}.png`), fullPage: true })
    })

    test(`customer code ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto(`/q/${publicToken}`)
      await reachCodeStep(page)
      await page.screenshot({ path: join(shotDir, `customer-code-${theme}-${width}.png`), fullPage: true })
    })

    test(`customer signed ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page, { publicState: 'signed' })
      await page.goto(`/q/${publicToken}`)
      await expect(page.getByTestId('public-signed')).toBeVisible()
      await expect(page.getByTestId('public-signed')).toContainText('Customer Signoff')
      await page.screenshot({ path: join(shotDir, `customer-signed-${theme}-${width}.png`), fullPage: true })
    })
  }
}

test('the customer approval becomes a dealer order', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto('/quotes')
  await page.getByTestId('preview-quote').click()
  await expect(page.getByTestId('preview-sheet')).toBeVisible()
  await page.getByTestId('preview-sheet').getByRole('button', { name: 'Close preview' }).click()
  await page.getByTestId('send-quote').click()
  const link = page.getByTestId('shared-link').locator('a')
  await expect(link).toBeVisible()
  await page.goto(await link.getAttribute('href') ?? `/q/${publicToken}`)
  await reachCodeStep(page)
  await page.getByTestId('verification-code').fill('123456')
  await page.getByTestId('public-accept').click()
  await expect(page.getByTestId('public-signed')).toContainText('Quote approved')
  await page.goto(`/quotes/${quoteIds.quote}`)
  await page.getByTestId('confirm-quote').click()
  await expect(page.locator('.pro-success')).toContainText('dealer order')
  await expect(page.getByTestId('quote-summary').locator('..').locator('..')).toContainText('SO-1001')
})
