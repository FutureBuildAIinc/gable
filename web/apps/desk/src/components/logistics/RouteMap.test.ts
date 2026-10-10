// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { makeStopIcon } from './RouteMap'

// RULE (PR 70 review round 1 P2-2): the stop icon compares the lowercase
// stop status from the wire. The legacy code compared the storage spelling
// (DELIVERED, FAILED, PARTIAL) so every stop rendered with the default
// colour regardless of its real status. The icon's HTML is the contract.
describe('makeStopIcon', () => {
  it.each([
    ['pending', '#00FFA3'],
    ['out_for_delivery', '#00FFA3'],
    ['delivered', '#10b981'],
    ['failed', '#ef4444'],
    ['partial', '#ef4444'],
  ])('paints %s with the %s colour', (status, expected) => {
    const icon = makeStopIcon(0, status)
    const html = icon.options.html as string
    expect(html).toContain(`background:${expected}`)
  })

  it('renders the open stops in dark text and the terminal stops in white', () => {
    const open = makeStopIcon(0, 'pending').options.html as string
    const terminal = makeStopIcon(0, 'delivered').options.html as string
    expect(open).toContain('color:#000')
    expect(terminal).toContain('color:#fff')
  })

  it('does not match the legacy uppercase storage spelling', () => {
    // The icon function must compare lowercase wire values. UPPERCASE here
    // proves the function is not silently treating storage values as wire
    // values (the live failure).
    const upper = makeStopIcon(0, 'DELIVERED').options.html as string
    expect(upper).toContain('background:#00FFA3')
  })
})