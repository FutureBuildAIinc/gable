// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { driverStatusClass, driverStatusColors } from './FleetManagement'

// RULE (PR 70 review round 1 P2-1): the colour map is the lowercase wire
// vocabulary, the active class is the fallback for an unknown status.
// The live failure: keys were the storage spelling (ACTIVE, INACTIVE,
// ON_LEAVE) so every driver row rendered a blank status badge.
describe('driverStatusClass', () => {
  it.each([
    ['active', 'emerald'],
    ['inactive', 'zinc'],
    ['on_leave', 'amber'],
  ])('returns the lowercase %s class for the %s status', (status, expected) => {
    expect(driverStatusClass(status)).toContain(expected)
  })

  it('falls back to the active class for an unknown status', () => {
    // A legacy uppercase value would have rendered blank; the fallback now
    // returns the active colour so the cell is never empty.
    expect(driverStatusClass('ACTIVE')).toBe(driverStatusColors.active)
    expect(driverStatusClass('')).toBe(driverStatusColors.active)
    expect(driverStatusClass('something_else')).toBe(driverStatusColors.active)
  })

  it('keys the colour map by the lowercase wire vocabulary', () => {
    for (const k of Object.keys(driverStatusColors)) {
      expect(k).toBe(k.toLowerCase())
    }
  })
})