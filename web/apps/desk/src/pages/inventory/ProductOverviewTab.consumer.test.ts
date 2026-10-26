// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The product's consumer durable flag (CP-17a): the overview card shows the
// flag's state in words, and the toggle PATCHes it on the revision the page
// holds.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const svc = vi.hoisted(() => ({ updateConsumerDurable: vi.fn() }));

vi.mock('../../services/product.service.ts', async (orig) => {
    const real = await orig<typeof import('../../services/product.service.ts')>();
    return { ...real, ProductService: { ...real.ProductService, updateConsumerDurable: svc.updateConsumerDurable } };
});

import '../inventory/tabs/ProductOverviewTab';
import type { GableProductOverviewTab } from '../inventory/tabs/ProductOverviewTab';
import type { ProductDetail } from '../../../types/pim.ts';

const detail = (consumerDurable: boolean): ProductDetail => ({
    product: {
        id: 'p-1', sku: 'STUD-2X4', description: '2x4 stud', stock_uom: 'PCS',
        base_price_ten_thousandths: 55000, average_unit_cost_ten_thousandths: 35000,
        target_margin: 0, commission_rate: 0, vendor: null, vendor_id: null, upc: null,
        weight_lbs: 0, length_in: null, width_in: null, height_in: null, stackable: null,
        geometry_source: null, lead_time_days: null, consumer_durable: consumerDurable,
        reorder_point: '0', reorder_qty: '0', on_hand: '0', allocated: '0', available: '0',
        revision: 4, created_at: '', updated_at: '', sale_uom: 'PCS', price_uom: 'PCS', purchase_uom: 'PCS',
        board_thickness_in: null, board_width_in: null, board_length_ft: null, random_length: false,
        units: [], image_url: null,
    },
} as unknown as ProductDetail);

async function mount(d: ProductDetail): Promise<GableProductOverviewTab> {
    const el = document.createElement('gable-product-overview-tab') as GableProductOverviewTab;
    el.detail = d;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

describe('the consumer durable flag', () => {
    beforeEach(() => { vi.clearAllMocks(); });

    it('says No for an ordinary product and Yes once set', async () => {
        const el = await mount(detail(false));
        const card = [...el.querySelectorAll('div')].find((d) => (d.textContent ?? '').includes('Consumer durable'));
        expect(card?.textContent).toContain('No');
        expect(card?.textContent).toContain("a lumber yard's default is off");
    });

    it('PATCHes the flag on the revision the page holds when toggled', async () => {
        svc.updateConsumerDurable.mockResolvedValue({});
        const el = await mount(detail(false));
        const toggle = [...el.querySelectorAll('button')].find((b) => b.title === 'Toggle the consumer durable flag') as HTMLButtonElement;
        toggle.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(svc.updateConsumerDurable).toHaveBeenCalledWith('p-1', true, 4);
    });
});
