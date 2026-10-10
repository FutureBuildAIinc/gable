// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { driverStatusClass, driverStatusColors } from './FleetManagement'

// RULE (PR 70 review round 1 P2-1): the colour map is the lowercase wire
// vocabulary the desk reads back (active, inactive, on_leave). The map
// keys being the storage spelling (ACTIVE, INACTIVE, ON_LEAVE) would have
// rendered every driver row with a blank status badge, since the wire
// values never matched a key. The map keys are the wire values instead,
// with the active class as the fallback for an unknown status.
describe('driverStatusClass', () => {
  it.each([
    ['active', 'emerald'],
    ['inactive', 'zinc'],
    ['on_leave', 'amber'],
  ])('returns the lowercase %s class for the %s status', (status, expected) => {
    expect(driverStatusClass(status)).toContain(expected)
  })

  it('falls back to the active class for an unknown status', () => {
    // An uppercase value is the storage spelling and would have rendered
    // blank before the wire-color map; the fallback returns the active
    // colour so the cell stays filled for any value the table does not
    // know.
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