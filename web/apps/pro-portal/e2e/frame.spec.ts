// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { expect, test } from '../../../e2e/font-guard'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { installPortalMocks, setTheme } from './fixtures.ts'

const shotDir = process.env.PRO_SHOT_DIR ?? '/tmp/gv1-t3-pro-shell-shots'
const widths = [390, 1024, 1440] as const
const themes = ['dark', 'light'] as const

test.beforeAll(() => {
  mkdirSync(shotDir, { recursive: true })
})

for (const theme of themes) {
  for (const width of widths) {
    test(`frame ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page)
      await page.goto('/today')
      await expect(page.getByTestId('today-screen')).toBeVisible()
      await expect(page.getByTestId('nav-today').first()).toHaveAttribute('aria-current', 'page')
      await page.screenshot({ path: join(shotDir, `frame-${theme}-${width}.png`), fullPage: true })
    })

    test(`sign in ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await setTheme(page, theme)
      await installPortalMocks(page, { signedIn: false })
      await page.goto('/today')
      await expect(page.getByTestId('sign-in-screen')).toBeVisible()
      await page.screenshot({ path: join(shotDir, `sign-in-${theme}-${width}.png`), fullPage: true })
      await page.getByLabel('Email').fill('project@example.com')
      await page.getByLabel('Password').fill('x')
      await page.getByRole('button', { name: 'Sign in', exact: true }).click()
      await expect(page.getByTestId('today-screen')).toBeVisible()
    })
  }
}
