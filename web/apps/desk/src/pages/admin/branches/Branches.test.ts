// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';

const loc = vi.hoisted(() => ({
    listAllBranches: vi.fn(),
    updateBranch: vi.fn(),
    createBranch: vi.fn(),
    archiveBranch: vi.fn(),
    locationUpdateFromLocation: vi.fn(() => ({})),
}));
const ctx = vi.hoisted(() => ({ refresh: vi.fn() }));

vi.mock('../../../services/LocationService', async (orig) => {
    const real = await orig<typeof import('../../../services/LocationService.ts')>();
    return { ...real, LocationService: loc, locationUpdateFromLocation: loc.locationUpdateFromLocation };
});
vi.mock('../../../services/BranchContext', () => ({ branchContext: ctx }));
vi.mock('../../../lib/toast-service', () => ({ ToastService: { show: vi.fn() } }));
vi.mock('../../../lib/router', () => ({ router: { navigate: vi.fn() } }));

import './Branches';
import { fractionToPercent, percentToFraction } from './Branches';
import type { GableAdminBranches } from './Branches';
import type { Location } from '../../../types/location';

const branch = (over: Partial<Location> = {}): Location => ({
    id: 'b1', parent_id: null, path: 'West', type: 'branch', code: 'WEST', description: null,
    name: 'West Yard', address: null, city: null, state: null, zip: null, phone: null,
    tax_jurisdiction_code: null, default_tax_rate: null, timezone: 'America/New_York',
    cash_rounding_mode: 'OFF', active: true, branch_id: null, revision: 4, created_at: '', updated_at: '',
    ...over,
} as Location);

async function mount(): Promise<GableAdminBranches> {
    loc.listAllBranches.mockResolvedValue([branch()]);
    const el = document.createElement('gable-admin-branches') as GableAdminBranches;
    document.body.appendChild(el);
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

async function openEditor(over: Partial<Location> = {}): Promise<GableAdminBranches> {
    loc.listAllBranches.mockResolvedValue([branch(over)]);
    const el = document.createElement('gable-admin-branches') as GableAdminBranches;
    document.body.appendChild(el);
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    (q(el, 'tbody tr') as HTMLElement).querySelectorAll('button')[1].click(); // Edit
    await el.updateComplete;
    return el;
}

const q = (el: Element, sel: string) => el.querySelector(sel) as HTMLElement;

describe('the branch form tax fields', () => {
    beforeEach(() => { vi.clearAllMocks(); document.body.innerHTML = ''; });

    it('converts the stored fraction to the percent a person types, and back', () => {
        expect(fractionToPercent(0.08875)).toBe('8.875');
        expect(fractionToPercent(0.12)).toBe('12');
        expect(fractionToPercent(null)).toBe('');
        expect(percentToFraction('8.875')).toBeCloseTo(0.08875);
        expect(percentToFraction('')).toBeNull();
        expect(percentToFraction('101')).toBeUndefined();
        expect(percentToFraction('-1')).toBeUndefined();
        expect(percentToFraction('half')).toBeUndefined();
    });

    it("shows the branch's own rate and jurisdiction, and saves both", async () => {
        const el = await mount();
        (q(el, 'tbody tr') as HTMLElement).querySelectorAll('button')[1].click(); // Edit
        await el.updateComplete;
        const inputs = [...el.querySelectorAll('input')] as HTMLInputElement[];
        const rateInput = inputs.find((i) => i.closest('label')?.textContent?.includes('Default tax rate')) as HTMLInputElement;
        const jurInput = inputs.find((i) => i.closest('label')?.textContent?.includes('Tax jurisdiction')) as HTMLInputElement;
        expect(rateInput).toBeTruthy();
        expect(jurInput).toBeTruthy();
        rateInput.value = '6.35';
        rateInput.dispatchEvent(new Event('input'));
        jurInput.value = 'CT-HARTFORD';
        jurInput.dispatchEvent(new Event('input'));
        await el.updateComplete;
        const save = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes')) as HTMLElement;
        save.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(loc.updateBranch).toHaveBeenCalledWith('b1', expect.objectContaining({
            default_tax_rate: 0.0635,
            tax_jurisdiction_code: 'CT-HARTFORD',
        }), 4);
        el.remove();
    });

    it('refuses a rate that is not a percent between 0 and 100, sending nothing', async () => {
        const el = await mount();
        (q(el, 'tbody tr') as HTMLElement).querySelectorAll('button')[1].click();
        await el.updateComplete;
        const inputs = [...el.querySelectorAll('input')] as HTMLInputElement[];
        const rateInput = inputs.find((i) => i.closest('label')?.textContent?.includes('Default tax rate')) as HTMLInputElement;
        rateInput.value = '150';
        rateInput.dispatchEvent(new Event('input'));
        await el.updateComplete;
        const save = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes')) as HTMLElement;
        save.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(loc.updateBranch).not.toHaveBeenCalled();
        el.remove();
    });
});

// The branch editor's own cash rounding rule (CP-18): a branch turns the rule
// on for itself, and the setting travels with the branch it belongs to.
describe('the branch editor cash rounding setting', () => {
    beforeEach(() => { vi.clearAllMocks(); document.body.innerHTML = ''; });

    it('shows the rule the branch carries, and the other one beside it', async () => {
        const el = await openEditor({ cash_rounding_mode: 'OFF' });
        const select = q(el, '[data-testid="branch-cash-rounding"]') as HTMLSelectElement;
        expect(select.value).toBe('OFF');
        expect(Array.from(select.options).map((o) => o.value)).toEqual(['OFF', 'NEAREST_5_CENTS']);
        el.remove();
    });

    it('saves the rule the manager picked', async () => {
        const el = await openEditor({ cash_rounding_mode: 'OFF' });
        const select = q(el, '[data-testid="branch-cash-rounding"]') as HTMLSelectElement;
        select.value = 'NEAREST_5_CENTS';
        select.dispatchEvent(new Event('change'));
        await el.updateComplete;
        const save = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes')) as HTMLElement;
        save.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(loc.updateBranch).toHaveBeenCalledWith('b1',
            expect.objectContaining({ cash_rounding_mode: 'NEAREST_5_CENTS' }), 4);
        el.remove();
    });

    it('leaves a branch that already rounds the cash showing its own rule', async () => {
        const el = await openEditor({ cash_rounding_mode: 'NEAREST_5_CENTS' });
        expect((q(el, '[data-testid="branch-cash-rounding"]') as HTMLSelectElement).value).toBe('NEAREST_5_CENTS');
        el.remove();
    });
});
