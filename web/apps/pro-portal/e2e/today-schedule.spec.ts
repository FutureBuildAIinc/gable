// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '../../../e2e/font-guard'
import { join } from 'node:path'
import { installPortalMocks, meFixture, setTheme } from './fixtures.ts'
import { testIds } from '../src/test/fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'
const widths = [390, 1024, 1440] as const
const themes = ['dark', 'light'] as const

for (const theme of themes) {
  for (const width of widths) {
    test(`Today ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/today')
      await expect(page.getByTestId('today-actions')).toBeVisible()
      await expect(page.getByTestId('today-count-orders')).toHaveText('1')
      await page.screenshot({ path: join(shotDir, `today-${theme}-${width}.png`), fullPage: true })
    })

    test(`Schedule ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/schedule')
      await expect(page.getByTestId('schedule-screen')).toBeVisible()
      await expect(page.getByTestId(`schedule-slot-${testIds.windowA}`)).toBeVisible()
      await page.screenshot({ path: join(shotDir, `schedule-${theme}-${width}.png`), fullPage: true })
    })

    test(`Timeline ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto(`/schedule/orders/${testIds.order}`)
      await expect(page.getByTestId('order-timeline')).toBeVisible()
      await expect(page.getByTestId('order-timeline').locator('li')).toHaveCount(5)
      await page.screenshot({ path: join(shotDir, `timeline-${theme}-${width}.png`), fullPage: true })
    })
  }
}

test('Today tiles open their lists', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto('/today')
  await page.getByTestId('today-tile-quotes').click()
  await expect(page.getByTestId('today-list-quotes')).toContainText('$73.88')
  await page.getByTestId('today-tile-invoices').click()
  await expect(page.getByTestId('today-list-invoices')).toContainText('$1,250.50')
})

test('site_super with see_money false sees zero dollar amounts on today and order detail', async ({ page }) => {
  const siteSuper = structuredClone(meFixture)
  siteSuper.user.role = 'site_super'
  siteSuper.capabilities = {
    manage_team: false,
    see_money: false,
    pay: false,
    order: false,
    request_quotes: false,
    approve_quotes: false,
    schedule: true,
    edit_delivery_instructions: true,
    quote_customers: false,
  }
  await installPortalMocks(page, { me: siteSuper })
  await page.goto('/today')
  await page.getByTestId('today-tile-orders').click()
  await expect(page.getByTestId('today-list-orders')).toBeVisible()
  await expect(page.getByTestId('today-list-orders')).not.toContainText('$')
  await expect(page.getByTestId('today-screen')).not.toContainText('$')

  await page.goto(`/schedule/orders/${testIds.order}`)
  await expect(page.getByTestId('order-detail-screen')).toBeVisible()
  await expect(page.getByTestId('order-detail-screen')).not.toContainText('$')
})

test('a pickup can be held at the other yard', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto('/schedule')
  await page.getByTestId('schedule-yard-select').click()
  await page.getByRole('option', { name: /North Yard/i }).click()
  await page.getByTestId('schedule-kind-pickup').click()
  await expect(page.getByTestId(`schedule-slot-${testIds.windowB}`)).toBeVisible()
  await page.getByTestId(`schedule-slot-${testIds.windowB}`).click()
  await page.getByTestId('schedule-hold').click()
  await expect(page.locator('.pro-success')).toContainText('changed')
})

test('site instructions save through the order route', async ({ page }) => {
  await installPortalMocks(page)
  await page.goto(`/schedule/orders/${testIds.order}`)
  await page.getByTestId('save-instructions').click()
  await expect(page.locator('.pro-success')).toContainText('site note is saved')
})
