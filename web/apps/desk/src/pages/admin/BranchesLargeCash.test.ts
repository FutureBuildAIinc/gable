// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The dealer's large cash settings card on the branches page (CP-17a): the
// default country select, the Canadian threshold switch and amount in
// dollars, the plain sentence that the Canadian capture is not a FINTRAC
// report, and the save round trip through the API.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.hoisted(() => vi.fn());
vi.mock('../../services/fetchClient', () => ({ fetchWithAuth: fetchMock }));

import './branches/Branches';
import type { GableAdminBranches } from './branches/Branches';

const SETTINGS = {
    country_default: '',
    ca_enabled: false,
    ca_threshold_cents: 1000000,
    ca_default_threshold_cents: 1000000,
};

let saved: unknown = null;

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function installFetch() {
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/admin/large-cash/settings') && init?.method === 'PUT') {
            saved = JSON.parse(String(init.body));
            return jsonResponse({ ...SETTINGS, ...(saved as object) });
        }
        if (url.includes('/api/v1/admin/large-cash/settings')) return jsonResponse(SETTINGS);
        if (url.includes('/api/v1/branches')) return jsonResponse({ items: [], next_cursor: null, limit: 50 });
        return jsonResponse({});
    });
}

beforeEach(() => {
    saved = null;
    installFetch();
});

async function mount(): Promise<GableAdminBranches> {
    const el = document.createElement('gable-admin-branches') as GableAdminBranches;
    document.body.appendChild(el);
    for (let i = 0; i < 20 && (el as unknown as { _largeCash: unknown })._largeCash === null; i++) {
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
    }
    await el.updateComplete;
    return el;
}

describe('the large cash settings card', () => {
    it('shows the dealer default country, the Canadian threshold and the plain sentence', async () => {
        const el = await mount();
        const words = el.textContent ?? '';
        expect(words).toContain('Dealer default country');
        expect(words).toContain('Canadian dealer threshold on');
        expect(words).toContain('It is not a FINTRAC report.');
        const amount = el.querySelector('input[type="number"]') as HTMLInputElement;
        expect(amount.value).toBe('10000.00');
    });

    it('saves the switched on threshold with the amount in cents and the chosen country', async () => {
        const el = await mount();
        const switchBox = [...el.querySelectorAll('label')]
            .find((l) => (l.textContent ?? '').includes('Canadian dealer threshold on'))
            ?.querySelector('input[type="checkbox"]') as HTMLInputElement;
        switchBox.checked = true;
        switchBox.dispatchEvent(new Event('change', { bubbles: true }));
        const amount = el.querySelector('input[type="number"]') as HTMLInputElement;
        amount.value = '25000';
        amount.dispatchEvent(new Event('input', { bubbles: true }));
        const select = el.querySelector('gable-select') as unknown as HTMLInputElement;
        select.value = 'CA';
        select.dispatchEvent(new Event('change', { bubbles: true }));
        await el.updateComplete;
        const save = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Save settings')) as HTMLButtonElement;
        save.click();
        for (let i = 0; i < 20 && saved === null; i++) await new Promise((r) => setTimeout(r, 0));
        expect(saved).toEqual(expect.objectContaining({
            ca_enabled: true, ca_threshold_cents: 2500000, country_default: 'CA',
        }));
    });
});
