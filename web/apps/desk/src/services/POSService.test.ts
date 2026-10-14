// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.hoisted(() => vi.fn());
vi.mock('./fetchClient', () => ({ fetchWithAuth: fetchMock }));

import { posService } from './POSService';

describe('the counter receipt routes', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('fetches the receipt through the authenticated client and opens a blob url, never the route', async () => {
    fetchMock.mockResolvedValue(new Response('%PDF', { status: 200, headers: { 'Content-Type': 'application/pdf' } }));
    const create = vi.fn().mockReturnValue('blob:receipt-1');
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    await posService.printReceipt('sale-1');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/pos/transactions/sale-1/receipt');
    expect(create).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith('blob:receipt-1', '_blank');
    expect(open.mock.calls.some((c) => String(c[0]).includes('/api/v1/'))).toBe(false);
    open.mockRestore();
  });

  it('says why when the receipt cannot be fetched and opens nothing', async () => {
    const body = { error: { code: 'conflict', message: 'this sale has no receipt: only a completed sale with an invoice has one' }, meta: { request_id: 'r' } };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(body), { status: 409, headers: { 'Content-Type': 'application/json' } }));
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    await expect(posService.printReceipt('sale-1')).rejects.toThrow('no receipt');
    expect(open).not.toHaveBeenCalled();
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
