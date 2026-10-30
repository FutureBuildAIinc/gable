// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Shared Playwright fixture (item 353): every shipped page bundles its fonts, so a
// test that makes a request to Google's font hosts fails. Specs import `test` and
// `expect` from here instead of `@playwright/test`.
import { expect, test as base } from '@playwright/test'

export const FONT_HOSTS = /^https?:\/\/fonts\.(googleapis|gstatic)\.com(\/|$)/

export const test = base.extend({
  context: async ({ context }, use) => {
    const hits: string[] = []
    context.on('request', (request) => {
      if (FONT_HOSTS.test(request.url())) hits.push(request.url())
    })
    await use(context)
    expect(hits, 'third party font requests (self host the fonts)').toEqual([])
  },
})

export * from '@playwright/test'
