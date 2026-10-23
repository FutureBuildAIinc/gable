// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { TaxExemption } from '../../types/tax';

const svc = vi.hoisted(() => ({
    list: vi.fn(),
    create: vi.fn(),
    end: vi.fn(),
}));

vi.mock('../../services/TaxExemptionService', async (orig) => {
    const real = await orig<typeof import('../../services/TaxExemptionService')>();
    return { ...real, TaxExemptionService: svc };
});

import './TaxExemptionPanel';
import type { GableTaxExemptionPanel } from './TaxExemptionPanel';
import { draftErrors, exemptionStatus } from './TaxExemptionPanel';
import { openAppDialog, answerAppDialog } from '../../test/dom';

const today = new Date().toISOString().slice(0, 10);

const cert = (over: Partial<TaxExemption> = {}): TaxExemption => ({
    id: 'e1', customer_id: 'c1', exempt_reason: 'RESALE', certificate_number: 'CERT-88',
    issuing_state: 'CT', effective_date: `${today}T00:00:00Z`, expiry_date: null,
    is_active: true, created_at: `${today}T00:00:00Z`, ...over,
});

async function mount(customerId = 'c1', list: TaxExemption[] = [cert()]): Promise<GableTaxExemptionPanel> {
    svc.list.mockResolvedValue(list);
    const el = document.createElement('gable-tax-exemption-panel') as GableTaxExemptionPanel;
    el.customerId = customerId;
    document.body.appendChild(el);
    await el.updateComplete;
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;

describe('the account tax exemption panel', () => {
    beforeEach(() => { vi.clearAllMocks(); document.body.innerHTML = ''; });

    it('lists the certificates on file with their status', async () => {
        const el = await mount();
        const rows = el.querySelectorAll('[data-testid="tax-exemption-row"]');
        expect(rows.length).toBe(1);
        expect(rows[0].textContent).toContain('CERT-88');
        expect(rows[0].textContent).toContain('RESALE');
        expect(rows[0].textContent).toContain('CT');
        expect(q(el, 'tax-exemption-status').textContent).toBe('Active');
        el.remove();
    });

    it('marks an expired certificate and one not yet in effect', async () => {
        const lastYear = String(Number(today.slice(0, 4)) - 1);
        const nextYear = String(Number(today.slice(0, 4)) + 1);
        const expired = cert({ id: 'e2', certificate_number: 'OLD-1', is_active: true,
            effective_date: '2020-01-01T00:00:00Z', expiry_date: `${lastYear}-12-31T00:00:00Z` });
        const future = cert({ id: 'e3', certificate_number: 'NEW-1',
            effective_date: `${nextYear}-01-01T00:00:00Z` });
        const el = await mount('c1', [expired, future]);
        const statuses = [...el.querySelectorAll('[data-testid="tax-exemption-status"]')].map((s) => s.textContent);
        expect(statuses).toEqual(['Expired', 'Not in effect yet']);
        el.remove();
    });

    it('adds a certificate through the dialog and refreshes the list', async () => {
        svc.create.mockResolvedValue(cert());
        const el = await mount();
        q(el, 'tax-exemption-add').click();
        await el.updateComplete;
        const dialog = el.querySelector('[data-testid="tax-exemption-dialog"]');
        expect(dialog).toBeTruthy();
        const inputs = dialog?.querySelectorAll('input') as NodeListOf<HTMLInputElement>;
        const byId = (id: string) => [...inputs].find((i) => i.id === id) as HTMLInputElement;
        byId('tax-exempt-number').value = 'CERT-99';
        byId('tax-exempt-number').dispatchEvent(new Event('input'));
        byId('tax-exempt-reason').value = 'RESALE';
        byId('tax-exempt-reason').dispatchEvent(new Event('input'));
        await el.updateComplete;
        q(el, 'tax-exemption-save').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(svc.create).toHaveBeenCalledWith(expect.objectContaining({
            customer_id: 'c1', certificate_number: 'CERT-99', exempt_reason: 'RESALE',
            effective_date: today,
        }));
        el.remove();
    });

    it('refuses an empty certificate number and a bad date in words, sending nothing', async () => {
        const el = await mount();
        q(el, 'tax-exemption-add').click();
        await el.updateComplete;
        const dialog = el.querySelector('[data-testid="tax-exemption-dialog"]') as HTMLElement;
        const inputs = dialog.querySelectorAll('input');
        const byId = (id: string) => [...inputs].find((i) => i.id === id) as HTMLInputElement;
        byId('tax-exempt-number').value = '';
        byId('tax-exempt-number').dispatchEvent(new Event('input'));
        byId('tax-exempt-reason').value = 'RESALE';
        byId('tax-exempt-reason').dispatchEvent(new Event('input'));
        byId('tax-exempt-effective').value = '31/12/2026';
        byId('tax-exempt-effective').dispatchEvent(new Event('input'));
        await el.updateComplete;
        q(el, 'tax-exemption-save').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(svc.create).not.toHaveBeenCalled();
        expect(el.textContent).toContain('The certificate number is required.');
        expect(el.textContent).toContain('The effective day is a date, YYYY-MM-DD.');
        el.remove();
    });

    it('ends a certificate only once confirmed', async () => {
        svc.end.mockResolvedValue(undefined);
        const el = await mount();
        q(el, 'tax-exemption-end').click();
        await new Promise((r) => setTimeout(r, 0));
        const dialog = openAppDialog();
        expect(dialog).toBeTruthy();
        expect(dialog?.textContent).toContain('End this certificate?');
        await answerAppDialog(false);
        expect(svc.end).not.toHaveBeenCalled();
        q(el, 'tax-exemption-end').click();
        await new Promise((r) => setTimeout(r, 0));
        await answerAppDialog(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(svc.end).toHaveBeenCalledWith('e1');
        el.remove();
    });
});

describe('the certificate window rule as the panel states it', () => {
    const status = exemptionStatus;
    const errors = draftErrors;

    it("states the status in the counter's own words", () => {
        expect(status(cert(), today)).toBe('Active');
        expect(status(cert({ is_active: false }), today)).toBe('Ended');
        expect(status(cert({ effective_date: '2999-01-01T00:00:00Z' }), today)).toBe('Not in effect yet');
        expect(status(cert({ expiry_date: '2000-01-01T00:00:00Z' }), today)).toBe('Expired');
    });

    it('refuses an expiry before the effective day', () => {
        const errs = errors({ certificate_number: 'C-1', exempt_reason: 'RESALE', issuing_state: 'CT',
            effective_date: '2026-06-01', expiry_date: '2026-05-01' });
        expect(errs.expiry_date).toContain('on or after');
    });
});
