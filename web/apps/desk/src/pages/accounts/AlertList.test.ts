// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The account page's Alerts panel: the office leaves a customer a note or a
 * warning, sees what is there and ends it. Only a warning may ask the counter
 * to acknowledge it, so the panel does not offer that on a note.
 */
import { describe, it, expect, afterEach, vi } from 'vitest';
import './AlertList';
import type { GableAlertList } from './AlertList';
import { mountAsync, text as textOf, q, flush, answerAppDialog } from '../../test/dom';
import { routedFetch } from '../../test/salesFixtures';

const CUSTOMER_ID = '00000000-0000-4000-8000-0000000000c1';

function alert(over: Record<string, unknown> = {}) {
    return {
        id: '00000000-0000-4000-8000-0000000000a1',
        customer_id: CUSTOMER_ID,
        kind: 'note',
        text: 'Call before the delivery',
        show_at_counter: true,
        show_at_orders: false,
        acknowledge_required: false,
        created_by: 'office',
        created_at: '2031-03-09T08:00:00Z',
        ended_by: null,
        ended_at: null,
        revision: 1,
        ...over,
    };
}

const NOTE = alert();
const WARNING = alert({
    id: '00000000-0000-4000-8000-0000000000a2',
    kind: 'warning',
    text: 'Needs a PO on every order',
    show_at_orders: true,
    acknowledge_required: true,
});

describe('the account page Alerts panel', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('lists the alerts the customer has, saying where each one speaks', async () => {
        const state = routedFetch({
            [`/api/v1/customers/${CUSTOMER_ID}/alerts`]: () => ({ items: [WARNING, NOTE], next_cursor: null, limit: 200 }),
        });
        vi.stubGlobal('fetch', vi.fn(state.fn));

        const el = await mountAsync<GableAlertList>('gable-alert-list', { customerId: CUSTOMER_ID } as never);
        expect(textOf(el)).toContain('Call before the delivery');
        expect(textOf(el)).toContain('Needs a PO on every order');
        expect(textOf(el)).toContain('At the counter');
        expect(textOf(el)).toContain('On orders');
        expect(el.querySelectorAll('[data-testid="alert-row"]').length).toBe(2);
        expect(el.querySelector('[data-testid="alert-row"][data-alert-kind="warning"]')).toBeTruthy();
    });

    it('says so when the customer has no alert', async () => {
        const state = routedFetch({
            [`/api/v1/customers/${CUSTOMER_ID}/alerts`]: () => ({ items: [], next_cursor: null, limit: 200 }),
        });
        vi.stubGlobal('fetch', vi.fn(state.fn));

        const el = await mountAsync<GableAlertList>('gable-alert-list', { customerId: CUSTOMER_ID } as never);
        expect(textOf(el)).toMatch(/no alerts on this account/i);
    });

    it('leaves a note with the office flags, and the author is the signed-in user', async () => {
        const state = routedFetch({
            [`/api/v1/customers/${CUSTOMER_ID}/alerts`]: () => ({ items: [], next_cursor: null, limit: 200 }),
            [`POST /api/v1/customers/${CUSTOMER_ID}/alerts`]: () => NOTE,
        });
        vi.stubGlobal('fetch', vi.fn(state.fn));

        const el = await mountAsync<GableAlertList>('gable-alert-list', { customerId: CUSTOMER_ID } as never);
        q<HTMLButtonElement>(el, '[data-testid="alert-add"]').click();
        await el.updateComplete;

        const input = el.querySelector('input[id$="-text"]') as HTMLInputElement;
        input.value = 'Call before the delivery';
        input.dispatchEvent(new Event('input'));
        await el.updateComplete;

        q<HTMLButtonElement>(el, '[data-testid="alert-save"]').click();
        await flush();

        const added = state.calls.filter((c) => c.init?.method === 'POST');
        expect(added.length).toBe(1);
        const body = JSON.parse(String(added[0].init?.body));
        expect(body.kind).toBe('note');
        expect(body.text).toBe('Call before the delivery');
        // The author is never in the body: the server knows who signed in.
        expect(body.created_by).toBeUndefined();
    });

    it('offers the acknowledgement only on a warning', async () => {
        const state = routedFetch({
            [`/api/v1/customers/${CUSTOMER_ID}/alerts`]: () => ({ items: [], next_cursor: null, limit: 200 }),
            [`POST /api/v1/customers/${CUSTOMER_ID}/alerts`]: () => WARNING,
        });
        vi.stubGlobal('fetch', vi.fn(state.fn));

        const el = await mountAsync<GableAlertList>('gable-alert-list', { customerId: CUSTOMER_ID } as never);
        q<HTMLButtonElement>(el, '[data-testid="alert-add"]').click();
        await el.updateComplete;

        expect(el.querySelector('input[id$="-ack"]')).toBeNull();

        const select = el.querySelector('gable-select') as HTMLElement & { value: string };
        select.value = 'warning';
        select.dispatchEvent(new Event('change'));
        await el.updateComplete;

        expect(el.querySelector('input[id$="-ack"]')).toBeTruthy();
    });

    it('ends an alert on its own revision, and the row stays on the account', async () => {
        const state = routedFetch({
            [`/api/v1/customers/${CUSTOMER_ID}/alerts`]: () => ({ items: [NOTE], next_cursor: null, limit: 200 }),
            [`POST /api/v1/customers/${CUSTOMER_ID}/alerts/${NOTE.id}/end`]: () =>
                alert({ ended_by: 'office', ended_at: '2031-03-10T08:00:00Z', revision: 2 }),
        });
        vi.stubGlobal('fetch', vi.fn(state.fn));

        const el = await mountAsync<GableAlertList>('gable-alert-list', { customerId: CUSTOMER_ID } as never);
        q<HTMLButtonElement>(el, '[data-testid="alert-end"]').click();
        await flush();
        await answerAppDialog(true);

        const ended = state.calls.find((c) => c.url.pathname.endsWith('/end'));
        expect(ended).toBeTruthy();
        const headers = ended!.init?.headers as Headers;
        expect(headers.get('If-Match')).toBe('"1"');
    });
});
