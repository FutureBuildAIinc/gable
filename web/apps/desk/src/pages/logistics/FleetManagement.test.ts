// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { driverStatusClass, driverStatusColors } from './FleetManagement'
import type { Driver } from '../../types/delivery'
import { mount, flush } from '../../test/dom'

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

// The render test for the drivers tab (PR 70 review round 3 P3-N5 /
// round 5 P3-2): the helper is covered above, but the render path that
// actually wires it was not. Replacing the render call with a hard
// coded status (driverStatusClass("ACTIVE")) would leave the unit tests
// green and leave the table rendering every driver row with the active
// colour. The test mounts the FleetManagement element, seeds the
// service with three drivers of distinct statuses, switches to the
// drivers tab, and asserts each row's status span carries the colour
// class that matches the driver's own status, not a single hard coded
// one. The fix for the r5 mutant (C4 in the mutant table) lives here.
const drivers: Driver[] = [
  {
    id: 'd-active',
    name: 'Ada Active',
    status: 'active',
    revision: 1,
    created_at: '',
    updated_at: '',
  },
  {
    id: 'd-leave',
    name: 'Lee Leave',
    status: 'on_leave',
    revision: 1,
    created_at: '',
    updated_at: '',
  },
  {
    id: 'd-inactive',
    name: 'Ina Inactive',
    status: 'inactive',
    revision: 1,
    created_at: '',
    updated_at: '',
  },
]

const fetchMock = vi.fn()
vi.mock('../../services/fetchClient', () => ({
  fetchWithAuth: (url: string) => fetchMock(url),
}))

vi.mock('../../services/deliveryService', () => ({
  deliveryService: {
    listVehicles: async () => [],
    listDrivers: async () => drivers,
    uploadVehiclePhoto: async () => '',
    uploadDriverPhoto: async () => '',
  },
}))

describe('FleetManagement render wires the driver status colour from the row', () => {
  beforeEach(() => {
    fetchMock.mockReset()
  })

  it('renders each driver row with the colour class matching its own status', async () => {
    const el = await mount('gable-fleet-management')
    await flush()
    // Open the drivers tab.
    const driversBtn = Array.from(el.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Drivers'),
    ) as HTMLElement | undefined
    driversBtn?.click()
    await flush()
    await el.updateComplete

    const rowsByName: Record<string, HTMLElement | null> = {
      'Ada Active': null,
      'Lee Leave': null,
      'Ina Inactive': null,
    }
    el.querySelectorAll('tbody tr').forEach((row) => {
      const nameCell = row.querySelector('td:nth-child(2)')
      const name = nameCell?.textContent?.trim()
      if (name && rowsByName[name] !== undefined) {
        rowsByName[name] = row as HTMLElement
      }
    })
    expect(rowsByName['Ada Active']).not.toBeNull()
    expect(rowsByName['Lee Leave']).not.toBeNull()
    expect(rowsByName['Ina Inactive']).not.toBeNull()

    const activeSpan = rowsByName['Ada Active']?.querySelector('span')
    const leaveSpan = rowsByName['Lee Leave']?.querySelector('span')
    const inactiveSpan = rowsByName['Ina Inactive']?.querySelector('span')
    expect(activeSpan?.className).toContain('emerald')
    expect(leaveSpan?.className).toContain('amber')
    expect(inactiveSpan?.className).toContain('zinc')
    // The three rows must carry three distinct classes, never one
    // hard coded class (the r5 mutant C4).
    const distinct = new Set([
      activeSpan?.className,
      leaveSpan?.className,
      inactiveSpan?.className,
    ])
    expect(distinct.size).toBe(3)
  })
})