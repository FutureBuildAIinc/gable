// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The exchange and the branch country's large cash rule (CP-17a): the server
 * says what the exchange needs of the counter, the payer's details ride the
 * exchange's own body, and the manager's suspected avoidance mark rides it
 * too.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { CounterRegister, ExchangeFigures, QuickSearchResult, Sale } from '../../types/pos';

const svc = vi.hoisted(() => ({
    listRegisters: vi.fn(),
    currentTill: vi.fn(),
    startSale: vi.fn(),
    completeSale: vi.fn(),
    largeCashPreview: vi.fn(),
    searchProducts: vi.fn(),
    findCustomers: vi.fn(),
    attachCustomer: vi.fn(),
    addItem: vi.fn(),
    removeItem: vi.fn(),
    changeQuantity: vi.fn(),
    getSale: vi.fn(),
    openTill: vi.fn(),
    tillReport: vi.fn(),
    closeTill: vi.fn(),
    printReceipt: vi.fn(),
    emailReceipt: vi.fn(),
    standing: vi.fn(),
    returnReasons: vi.fn(),
    previewExchange: vi.fn(),
    createExchange: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
    const real = await orig<typeof import('../../services/POSService')>();
    return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import type { GablePayerDialog } from './PayerDialog';
import { flush } from '../../test/dom';

const REG_01: CounterRegister = {
    id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard',
};

const PRODUCT: QuickSearchResult = {
    product_id: 'p-1', sku: 'SKU-1', description: '2x4x8 SPF', unit_price_cents: 599, uom: 'EA', in_stock: '100',
};

const line = (id: string, cents: number) => ({
    id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
    charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
    quantity: '1', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function completedSale(): Sale {
    return {
        id: 's1', number: 'S-1', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
        customer_id: null, currency: 'USD', subtotal_cents: 1100, tax_cents: 98, total_cents: 1198,
        change_cents: 0, till_session_id: 't', status: 'completed', invoice_id: null, completed_at: null,
        created_at: '', customer_po: null, job_id: null, buyer_contact_id: null, print_count: 0,
        exchange_return_id: null, tenders: [], lines: [line('a', 1100)],
    } as unknown as Sale;
}

/** The figures the server states, with the large cash answer beside them. */
const figures = (over: Partial<ExchangeFigures> = {}): ExchangeFigures => ({
    returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 119800, difference_cents: 119201, ...over,
});

const ASKS = {
    country: 'US', rule_family: 'US_8300', counted_cents: 119201, aggregate_cents: 119201,
    threshold_cents: 1000000, comparator: 'greater', asks_details: true,
    fields: ['name', 'address', 'taxpayer_id', 'date_of_birth', 'occupation', 'id_type', 'id_number', 'third_party_payer'],
    dealer_threshold: false,
};

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;

type Internals = {
    _beginReturn: (sale?: Sale) => void;
    _suspectAvoidance: boolean;
};
const inner = (el: POSTerminal) => el as unknown as Internals;

/** Fill the payer's capture in the dialog the counter is asked in. */
function fillPayer(el: Element) {
    const fill = (testid: string, value: string) => {
        const input = el.querySelector(`[data-testid="${testid}"]`) as HTMLInputElement;
        input.value = value;
        input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    fill('payer-name', 'Riverbend Builders');
    fill('payer-address', '12 Mill Road, Kelowna');
    fill('payer-date_of_birth', '1984-05-06');
    fill('payer-occupation', 'Contractor');
    const idType = el.querySelector('[data-testid="payer-id_type"]') as HTMLSelectElement;
    idType.value = 'drivers_license';
    idType.dispatchEvent(new Event('change', { bubbles: true }));
    fill('payer-id_number', 'DL-00-1234567');
    fill('payer-taxpayer_id', '00-1234567');
}

async function mountTerminal(): Promise<POSTerminal> {
    svc.listRegisters.mockResolvedValue([REG_01]);
    svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
    svc.startSale.mockResolvedValue(completedSale());
    svc.returnReasons.mockResolvedValue([
        { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 1500, needs_note: false },
    ]);
    const el = document.createElement('gable-pos-terminal') as POSTerminal;
    document.body.appendChild(el);
    await flush();
    await el.updateComplete;
    return el;
}

/** The return dialog on a completed sale, turned into an exchange with one replacement line. */
async function openExchange(el: POSTerminal) {
    inner(el)._beginReturn(completedSale());
    await flush();
    await el.updateComplete;
    // one unit of the sale comes back
    const qty = q(el, 'return-qty') as HTMLInputElement;
    qty.value = '1';
    qty.dispatchEvent(new Event('input'));
    await el.updateComplete;
    // and why it comes back
    const reason = q(el, 'return-reason') as HTMLSelectElement;
    reason.value = 'changed_mind';
    reason.dispatchEvent(new Event('change', { bubbles: true }));
    await el.updateComplete;
    // the replacement goods
    q(el, 'return-exchange-toggle').click();
    await el.updateComplete;
    svc.searchProducts.mockResolvedValue([PRODUCT]);
    const search = q(el, 'exchange-search') as HTMLInputElement;
    search.value = '2x4';
    search.dispatchEvent(new Event('input'));
    await new Promise((resolve) => setTimeout(resolve, 250));
    await flush();
    await el.updateComplete;
    (q(el, 'exchange-results').querySelector('button') as HTMLElement).click();
    await el.updateComplete;
    // the preview is debounced
    await new Promise((resolve) => setTimeout(resolve, 350));
    await flush();
    await el.updateComplete;
}

describe('the exchange and the large cash rule of the branch country', () => {
    beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });

    it("asks the payer's details before the exchange is rung, and the capture rides the exchange body", async () => {
        svc.previewExchange.mockResolvedValue(figures({ large_cash: ASKS }));
        svc.createExchange.mockResolvedValue({
            return: { id: 'r1', number: 'R-1' }, sale: completedSale(),
            returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 119800,
            difference_cents: 119201, refunded_cents: 0,
        });
        const el = await mountTerminal();
        await openExchange(el);

        // the preview named the tender method, so the server could say what the rule needs
        expect(svc.previewExchange.mock.calls[0][0].tender_method).toBe('cash');

        q(el, 'exchange-take').click();
        await flush();
        await el.updateComplete;

        // the payer's details are asked for, in the fields the server named
        const dialog = document.querySelector('gable-payer-dialog') as GablePayerDialog | null;
        expect(dialog).toBeTruthy();
        expect(dialog!.ask?.fields).toContain('taxpayer_id');
        // and nothing is rung while the question is open
        expect(svc.createExchange).not.toHaveBeenCalled();

        // the capture answers it: the exchange is rung with the payer on its body
        fillPayer(dialog!);
        await dialog!.updateComplete;
        (dialog!.querySelector('[data-testid="payer-save"]') as HTMLButtonElement).click();
        await flush();
        await el.updateComplete;

        expect(svc.createExchange).toHaveBeenCalledTimes(1);
        const body = svc.createExchange.mock.calls[0][0];
        expect(body.payer).toMatchObject({ name: 'Riverbend Builders', taxpayer_id: '00-1234567' });
        expect(body.tenders).toEqual([{ method: 'cash', amount_cents: 119201 }]);
        el.remove();
    });

    it('carries the manager\'s suspected avoidance mark on the exchange body', async () => {
        svc.previewExchange.mockResolvedValue(figures());
        svc.createExchange.mockResolvedValue({
            return: { id: 'r1', number: 'R-1' }, sale: completedSale(),
            returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 119800,
            difference_cents: 119201, refunded_cents: 0,
        });
        const el = await mountTerminal();
        await openExchange(el);

        const mark = q(el, 'exchange-avoidance') as HTMLInputElement;
        expect(mark).toBeTruthy();
        mark.checked = true;
        mark.dispatchEvent(new Event('change'));
        await el.updateComplete;

        q(el, 'exchange-take').click();
        await flush();
        await el.updateComplete;

        expect(svc.createExchange).toHaveBeenCalledTimes(1);
        expect(svc.createExchange.mock.calls[0][0].suspected_avoidance).toBe(true);
        el.remove();
    });

    it("does not carry the manager's mark past the exchange it was taken for", async () => {
        svc.previewExchange.mockResolvedValue(figures());
        const el = await mountTerminal();
        await openExchange(el);

        const mark = q(el, 'exchange-avoidance') as HTMLInputElement;
        mark.checked = true;
        mark.dispatchEvent(new Event('change'));
        await el.updateComplete;
        expect(inner(el)._suspectAvoidance).toBe(true);

        // the clerk cancels the exchange: the mark belonged to it
        const row = q(el, 'return-exchange-toggle').parentElement as HTMLElement;
        const cancel = Array.from(row.querySelectorAll('button'))
            .find((b) => b.textContent?.trim() === 'Cancel') as HTMLElement;
        cancel.click();
        await el.updateComplete;

        expect(inner(el)._suspectAvoidance).toBe(false);
        el.remove();
    });

    it('asks nothing of an exchange under the threshold', async () => {        svc.previewExchange.mockResolvedValue(figures({
            large_cash: { ...ASKS, asks_details: false, fields: [] },
        }));
        svc.createExchange.mockResolvedValue({
            return: { id: 'r1', number: 'R-1' }, sale: completedSale(),
            returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 119800,
            difference_cents: 119201, refunded_cents: 0,
        });
        const el = await mountTerminal();
        await openExchange(el);

        q(el, 'exchange-take').click();
        await flush();
        await el.updateComplete;

        expect(document.querySelector('gable-payer-dialog')).toBeNull();
        expect(svc.createExchange).toHaveBeenCalledTimes(1);
        const body = svc.createExchange.mock.calls[0][0];
        expect(body.payer).toBeUndefined();
        expect(body.suspected_avoidance).toBeUndefined();
        el.remove();
    });
});
