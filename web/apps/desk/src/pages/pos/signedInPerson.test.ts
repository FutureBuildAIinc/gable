// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The person a till report names as the signed in cashier: a staff session's
 * subject is the id the server stamps on a drawer they open.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { authCustody } from '@gable/auth'
import { signedInPerson } from './tillReport.ts'

const SUB = '3f2b8c1e-0d5a-5b7e-9c41-6a1d2e3f4a5b'

beforeEach(() => { sessionStorage.clear(); authCustody.signOut() })
afterEach(() => { authCustody.signOut() })

describe('signedInPerson', () => {
  it('is the staff session\'s subject and the name typed at the door', () => {
    authCustody.signInStaff({ sub: SUB.toUpperCase(), roles: ['cashier'], name: 'till@example.test' })
    expect(signedInPerson()).toEqual({ id: SUB, name: 'till@example.test' })
  })

  it('has no id when signed out', () => {
    expect(signedInPerson()).toEqual({ id: null, name: null })
  })
})
