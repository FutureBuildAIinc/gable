// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.hoisted(() => vi.fn());
vi.mock('./fetchClient', () => ({ fetchWithAuth: fetchMock }));

import { posService } from './POSService';

describe('the counter receipt routes', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('opens the tab during the click, before the fetch answers, then points it at a blob url', async () => {
    let answer: (r: Response) => void = () => {};
    fetchMock.mockImplementation(() => new Promise<Response>((r) => { answer = r; }));
    const create = vi.fn().mockReturnValue('blob:receipt-1');
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() });
    const tab = { location: { href: '' }, close: vi.fn() };
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
    const pending = posService.printReceipt('sale-1');
    // synchronously, with the fetch still unanswered
    expect(open).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith('', '_blank');
    expect(create).not.toHaveBeenCalled();
    answer(new Response('%PDF', { status: 200, headers: { 'Content-Type': 'application/pdf' } }));
    const result = await pending;
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/pos/transactions/sale-1/receipt');
    expect(tab.location.href).toBe('blob:receipt-1');
    expect(result).toEqual({ url: 'blob:receipt-1', blocked: false });
    open.mockRestore();
  });

  it('answers a blocked tab with the blob url so the page can offer a download', async () => {
    fetchMock.mockResolvedValue(new Response('%PDF', { status: 200 }));
    Object.assign(URL, { createObjectURL: vi.fn().mockReturnValue('blob:receipt-2'), revokeObjectURL: vi.fn() });
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    await expect(posService.printReceipt('sale-1')).resolves.toEqual({ url: 'blob:receipt-2', blocked: true });
    open.mockRestore();
  });

  it('closes the tab and throws the words when the receipt cannot be fetched', async () => {
    const body = { error: { code: 'conflict', message: 'this sale has no receipt: only a completed sale with an invoice has one' }, meta: { request_id: 'r' } };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(body), { status: 409, headers: { 'Content-Type': 'application/json' } }));
    const tab = { location: { href: '' }, close: vi.fn() };
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
    await expect(posService.printReceipt('sale-1')).rejects.toThrow('no receipt');
    expect(tab.close).toHaveBeenCalledTimes(1);
    expect(tab.location.href).toBe('');
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
