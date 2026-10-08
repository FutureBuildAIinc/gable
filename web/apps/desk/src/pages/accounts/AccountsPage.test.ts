// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The accounts list's credit/cash split: the server has no credit filter, so the page splits the
 * loaded rows. Credit means no limit (null) or a limit above 0; cash means a limit of exactly 0.
 */
import { describe, it, expect } from 'vitest'
import { matchesCreditFilter } from './AccountsPage'

describe('matchesCreditFilter', () => {
  it('all keeps every row', () => {
    for (const v of [null, 0, 1, 500000]) expect(matchesCreditFilter('all', v)).toBe(true)
  })

  it('credit is a null limit or a limit above zero', () => {
    expect(matchesCreditFilter('credit', null)).toBe(true)
    expect(matchesCreditFilter('credit', 1)).toBe(true)
    expect(matchesCreditFilter('credit', 0)).toBe(false)
  })

  it('cash is a limit of exactly zero, never a null limit', () => {
    expect(matchesCreditFilter('cash', 0)).toBe(true)
    expect(matchesCreditFilter('cash', null)).toBe(false)
    expect(matchesCreditFilter('cash', 100)).toBe(false)
  })
})
