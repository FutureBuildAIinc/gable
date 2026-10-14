// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.hoisted(() => vi.fn());
vi.mock('./fetchClient', () => ({ fetchWithAuth: fetchMock }));

import { posService } from './POSService';

describe('the counter receipt routes', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('opens the sale\'s own receipt document in a new tab', () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    posService.printReceipt('sale-1');
    expect(open).toHaveBeenCalledWith('/api/v1/pos/transactions/sale-1/receipt', '_blank');
    open.mockRestore();
  });

  it('posts the email to the sale\'s receipt route and takes the queued 202', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ status: 'queued' }), { status: 202 }));
    await posService.emailReceipt('sale-1');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/pos/transactions/sale-1/receipt/email');
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'POST' });
  });

  it('throws the server\'s words when the account has no contact', async () => {
    const body = { error: { code: 'no_contact', message: 'This account has no email address on file' }, meta: { request_id: 'r' } };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(body), { status: 409, headers: { 'Content-Type': 'application/json' } }));
    await expect(posService.emailReceipt('sale-1')).rejects.toThrow('no email address');
  });
});
