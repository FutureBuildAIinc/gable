// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const tint = vi.hoisted(() => ({
    systems: vi.fn(),
    bases: vi.fn(),
    colors: vi.fn(),
    formulas: vi.fn(),
    rescale: vi.fn(),
    charge: vi.fn(),
    savedColors: vi.fn(),
    saveColor: vi.fn(),
}));
const pos = vi.hoisted(() => ({ addItemTint: vi.fn() }));

vi.mock('../../services/TintService', async (orig) => {
    const real = await orig<typeof import('../../services/TintService')>();
    return { ...real, tintService: tint };
});
vi.mock('../../services/POSService', async (orig) => {
    const real = await orig<typeof import('../../services/POSService')>();
    return { ...real, posService: pos };
});

import './TintLineDialog.ts';
import type { TintLineDialog } from './TintLineDialog';
import { mount, q, chooseOption } from '../../test/dom';

const SYSTEM = { id: 'sys', code: 'GEN', name: 'Generic', manufacturer: 'Generic Paints', unit_denominator: 384, unit_label: '' };
const BASE = { id: 'base', tint_system_id: 'sys', code: 'DEEP', name: 'Deep base', manufacturer_base_code: '', product_id: 'p1', fill_volume_oz: 122, is_deepest: false };
const COLOR = { id: 'color', tint_system_id: 'sys', manufacturer_code: 'HC-188', name: 'Essex Green', is_custom: false };
const GAL = {
    id: 'gal', color_id: 'color', base_id: 'base', container_size_code: 'GAL', container_volume_oz: 128,
    components: [{ colorant_code: 'B1', colorant_name: 'Black', amount: { whole_oz: 1, increments_coarse: 0, increments_fine: 0, unit_denominator: 384 } }],
};

const tick = async (el: TintLineDialog) => {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
};

let el: TintLineDialog;

async function open(customerId = '') {
    el = await mount<TintLineDialog>('gable-tint-line-dialog', {
        saleId: 'sale', productId: 'p1', basePrice: 55000, customerId,
    });
    await tick(el);
    return el;
}

beforeEach(() => {
    tint.systems.mockReset().mockResolvedValue({ items: [SYSTEM] });
    tint.bases.mockReset().mockResolvedValue({ items: [BASE] });
    tint.colors.mockReset().mockResolvedValue({ items: [COLOR] });
    tint.formulas.mockReset().mockResolvedValue({ items: [GAL] });
    tint.charge.mockReset().mockResolvedValue({ tint_charge_cents: 250 });
    tint.savedColors.mockReset().mockResolvedValue({ items: [] });
    tint.saveColor.mockReset().mockResolvedValue({});
    pos.addItemTint.mockReset();
});
afterEach(() => { document.body.innerHTML = ''; });

describe('gable-tint-line-dialog', () => {
    it('loads the base and offers the colours of its system', async () => {
        await open();
        expect(tint.bases).toHaveBeenCalledWith({ product_id: 'p1' });
        expect(tint.colors).toHaveBeenCalledWith({ tint_system_id: 'sys', q: '' });
    });

    it('choosing a size the book does not hold rescales and shows the rounding flag', async () => {
        await open();
        // pick the colour: the gallon formula loads
        (q(el, '[data-testid="tint-color-option"]') as HTMLElement).click();
        await tick(el);
        expect(q(el, '[data-testid="tint-components"]')).toBeTruthy();
        // the quart is offered beside the gallon; choosing it rescales
        tint.rescale.mockResolvedValue({
            formula: GAL, target_volume_oz: 32, any_rounded: true,
            components: [{ colorant_code: 'B1', colorant_name: 'Black', rounded: true, amount: { whole_oz: 0, increments_coarse: 2, increments_fine: 0, unit_denominator: 384 } }],
        });
        await chooseOption(q(el, '[data-testid="tint-size"]'), 'QT');
        await tick(el);
        expect(tint.rescale).toHaveBeenCalledWith('gal', 32);
        expect(q(el, '[data-testid="tint-rescaled"]')).toBeTruthy();
        expect(q(el, '[data-testid="tint-price"]').textContent).toContain('$8.00');
    });

    it('editing a colorant amount updates the shown amount', async () => {
        await open();
        (q(el, '[data-testid="tint-color-option"]') as HTMLElement).click();
        await tick(el);
        const coarse = q<HTMLInputElement>(el, '[data-testid="tint-coarse-0"]');
        coarse.value = '12';
        coarse.dispatchEvent(new Event('input', { bubbles: true }));
        await tick(el);
        expect(el.querySelector('[data-testid="tint-components"]')?.textContent).toContain('1 OZ + 12/32');
    });

    it('sends the tint with the line on Add', async () => {
        pos.addItemTint.mockResolvedValue({ id: 'sale' });
        await open();
        (q(el, '[data-testid="tint-color-option"]') as HTMLElement).click();
        await tick(el);
        (q(el, '[data-testid="tint-add"]') as HTMLElement).click();
        await tick(el);
        expect(pos.addItemTint).toHaveBeenCalledWith('sale', 'p1', expect.objectContaining({
            color_code: 'HC-188', color_name: 'Essex Green', base_code: 'DEEP', container_size_code: 'GAL',
            components: [expect.objectContaining({ colorant_code: 'B1' })],
        }));
    });

    it('a saved colour fills the whole dialog', async () => {
        tint.savedColors.mockResolvedValue({ items: [{ id: 's1', customer_id: 'c1', color_id: 'color', name: 'Living room', job_note: 'living room', note: '' }] });
        await open('c1');
        await tick(el);
        const saved = el.querySelector('[data-testid="tint-saved-colors"] button') as HTMLElement;
        saved.click();
        await tick(el);
        expect(q<HTMLInputElement>(el, '[data-testid="tint-save-name"]').value).toBe('Living room');
        expect(q(el, '[data-testid="tint-components"]')).toBeTruthy();
    });
});
