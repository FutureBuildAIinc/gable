// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The branch act's walk-in identity floor on the branches page (CP-17a, item
// 296): the amount the counter asks a walk-in's identity over, the branch
// country's default it starts from, and the adviser's note that says the
// walk-in's cash is not aggregated with the floor off.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.hoisted(() => vi.fn());
vi.mock('../../services/fetchClient', () => ({ fetchWithAuth: fetchMock }));

import './branches/Branches';
import type { GableAdminBranches } from './branches/Branches';

const SETTINGS = {
    country_default: 'US',
    ca_enabled: false,
    ca_threshold_cents: 1000000,
    ca_default_threshold_cents: 1000000,
};

const BRANCH = {
    id: 'b-1', parent_id: null, path: 'WEST', type: 'branch', code: 'WEST', description: null,
    name: 'West Yard', address: null, city: null, state: null, zip: null, phone: null,
    tax_jurisdiction_code: null, default_tax_rate: null, cash_rounding_mode: 'OFF',
    timezone: 'America/New_York', country_code: 'US', walk_in_identity_cents: null,
    active: true, branch_id: null, revision: 4, created_at: '', updated_at: '',
};

let saved: unknown = null;

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

beforeEach(() => {
    saved = null;
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/api/v1/admin/large-cash/settings')) return jsonResponse(SETTINGS);
        if (url.includes('/api/v1/branches') && init?.method === 'PUT') {
            saved = JSON.parse(String(init.body));
            return jsonResponse({ ...BRANCH, ...(saved as object) });
        }
        if (url.includes('/api/v1/branches')) {
            return jsonResponse({ items: [BRANCH], next_cursor: null, limit: 50 });
        }
        return jsonResponse({});
    });
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

async function openEditor(el: GableAdminBranches): Promise<void> {
    const edit = [...el.querySelectorAll('button')].find((b) => b.getAttribute('title') === 'Edit') as HTMLButtonElement;
    edit.click();
    await el.updateComplete;
}

describe("the branch act's walk-in identity floor", () => {
    it('starts from the branch country default and says the walk-in cash is counted together', async () => {
        const el = await mount();
        await openEditor(el);
        const on = el.querySelector('[data-testid="branch-walk-in-floor-on"]') as HTMLInputElement;
        const amount = el.querySelector('[data-testid="branch-walk-in-floor"]') as HTMLInputElement;
        const note = el.querySelector('[data-testid="branch-walk-in-floor-note"]');
        expect(on.checked).toBe(true);
        expect(amount.value).toBe('3000.00');
        expect(note?.textContent).toContain('counted together');
        expect(note?.textContent).toContain('The United States default is 3,000.00 dollars.');
    });

    it('says in the adviser note that the walk-in cash is not aggregated with the floor off', async () => {
        const el = await mount();
        await openEditor(el);
        const on = el.querySelector('[data-testid="branch-walk-in-floor-on"]') as HTMLInputElement;
        on.checked = false;
        on.dispatchEvent(new Event('change', { bubbles: true }));
        await el.updateComplete;
        const note = el.querySelector('[data-testid="branch-walk-in-floor-note"]');
        expect(note?.textContent).toContain('walk-in cash is not aggregated at this branch');
    });

    it('saves the floor off as zero cents, and the branch own amount as cents', async () => {
        const el = await mount();
        await openEditor(el);
        const on = el.querySelector('[data-testid="branch-walk-in-floor-on"]') as HTMLInputElement;
        on.checked = false;
        on.dispatchEvent(new Event('change', { bubbles: true }));
        await el.updateComplete;
        const save = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes')) as HTMLButtonElement;
        save.click();
        for (let i = 0; i < 20 && saved === null; i++) await new Promise((r) => setTimeout(r, 0));
        expect(saved).toEqual(expect.objectContaining({ walk_in_identity_cents: 0 }));

        saved = null;
        const el2 = await mount();
        await openEditor(el2);
        const on2 = el2.querySelector('[data-testid="branch-walk-in-floor-on"]') as HTMLInputElement;
        on2.checked = true;
        on2.dispatchEvent(new Event('change', { bubbles: true }));
        const amount = el2.querySelector('[data-testid="branch-walk-in-floor"]') as HTMLInputElement;
        amount.value = '2500';
        amount.dispatchEvent(new Event('input', { bubbles: true }));
        await el2.updateComplete;
        const save2 = [...el2.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes')) as HTMLButtonElement;
        save2.click();
        for (let i = 0; i < 20 && saved === null; i++) await new Promise((r) => setTimeout(r, 0));
        expect(saved).toEqual(expect.objectContaining({ walk_in_identity_cents: 250000 }));
    });
});
