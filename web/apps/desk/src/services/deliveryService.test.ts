// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, vi, beforeEach } from 'vitest'

// The wire rule (PR 70 review round 1 P3-1): the desk client carries the
// revision the edit form loaded. A fresh re-read inside the service would
// defeat the revision the user was editing on. The test asserts the service
// sends the caller's revision, never one it fetched itself.

interface UpdateVehiclePayload { name: string; vehicle_type: string; license_plate: string }
interface UpdateDriverPayload { name: string; status: string }
interface TransitionPayload { to: string }

const fetchMock = vi.fn()
vi.mock('./fetchClient', () => ({
  fetchWithAuth: (url: string, init: RequestInit = {}) => fetchMock(url, init),
}))
vi.mock('./apiError', () => ({
  parseApiError: async () => new Error('parse error'),
}))

import { deliveryService } from './deliveryService'

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    headers: new Headers(),
    json: async () => ({ revision: 7 }),
  })
})

describe('deliveryService carries the loaded revision', () => {
  it('updateVehicle sends the caller revision, not a fresh read', async () => {
    const body: UpdateVehiclePayload = { name: 'x', vehicle_type: 'van', license_plate: 'p' }
    await deliveryService.updateVehicle('v1', body, 4)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0]
    const parsed = JSON.parse(String(init.body))
    expect(parsed.revision).toBe(4)
  })

  it('deleteVehicle sends If-Match header from the caller revision', async () => {
    await deliveryService.deleteVehicle('v1', 4)
    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Record<string, string>)['If-Match']).toBe('"4"')
  })

  it('updateDriver sends the caller revision', async () => {
    const body: UpdateDriverPayload = { name: 'y', status: 'active' }
    await deliveryService.updateDriver('d1', body, 5)
    const [, init] = fetchMock.mock.calls[0]
    expect(JSON.parse(String(init.body)).revision).toBe(5)
  })

  it('deleteDriver sends If-Match from the caller revision', async () => {
    await deliveryService.deleteDriver('d1', 5)
    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Record<string, string>)['If-Match']).toBe('"5"')
  })

  it('reorderStops sends If-Match from the caller revision', async () => {
    await deliveryService.reorderStops('r1', ['a', 'b'], 9)
    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Record<string, string>)['If-Match']).toBe('"9"')
  })

  it('optimizeRoute sends If-Match from the caller revision', async () => {
    await deliveryService.optimizeRoute('r1', 9)
    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Record<string, string>)['If-Match']).toBe('"9"')
  })

  it('transitionRoute sends the caller revision in the body', async () => {
    await deliveryService.transitionRoute('r1', 'completed', 3)
    const [, init] = fetchMock.mock.calls[0]
    expect(JSON.parse(String(init.body)).revision).toBe(3)
  })

  it('updateStatus sends the caller revision in the body', async () => {
    const body: TransitionPayload = { to: 'delivered' }
    await deliveryService.updateStatus('s1', body, 6)
    const [, init] = fetchMock.mock.calls[0]
    expect(JSON.parse(String(init.body)).revision).toBe(6)
  })

  it('does not issue a fresh read before any of these writes', async () => {
    const vBody: UpdateVehiclePayload = { name: '', vehicle_type: '', license_plate: '' }
    const dBody: UpdateDriverPayload = { name: '', status: '' }
    const sBody: TransitionPayload = { to: '' }
    await deliveryService.updateVehicle('v1', vBody, 1)
    await deliveryService.deleteVehicle('v1', 1)
    await deliveryService.updateDriver('d1', dBody, 1)
    await deliveryService.deleteDriver('d1', 1)
    await deliveryService.reorderStops('r1', ['a'], 1)
    await deliveryService.optimizeRoute('r1', 1)
    await deliveryService.transitionRoute('r1', 'in_transit', 1)
    await deliveryService.updateStatus('s1', sBody, 1)
    // 8 calls, one per write. A fresh GET inside the service would push
    // this to 16 or more.
    expect(fetchMock.mock.calls.length).toBe(8)
  })
})