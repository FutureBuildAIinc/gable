// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './TillReportView';
import './TillZReports';
import type { TillReportView } from './TillReportView';
import type { TillZReports } from './TillZReports';
import type { TillReport, ZReport } from '../../types/pos';
import { flush, jsonResponse, mount, q, text, update } from '../../test/dom';
import { cashierLabel, expectedDrawerCash, isBlind, reportMethods } from './tillReport';

const session = (over: Record<string, unknown> = {}) => ({
    id: 's-1', register_id: 'REG-01', branch_id: 'b-1', cashier_id: 'u-1', status: 'open',
    opening_float_cents: 20000, opened_at: '2026-06-01T09:00:00Z', closed_at: null,
    expected_by_method: {}, counted_by_method: {}, over_short_cents: null, gl_entry_id: null,
    notes: '', closed_by: null, variance_threshold_cents: null, sign_off: null, ...over,
});

/** A manager's read of the open drawer: every money figure present. */
const managerReport: TillReport = {
    session: session({ expected_by_method: { CASH: 21950, CHECK: 4000 } }) as TillReport['session'],
    sale_count: 3, sales_total_cents: 5950, tax_total_cents: 450, change_cents: 0,
    tendered_by_method: { CASH: 1950, CHECK: 4000 },
    expected_by_method: { CASH: 21950, CHECK: 4000 },
    refunds_by_method: { CASH: 450 },
    movements: [{
        id: 'm-1', till_session_id: 's-1', register_id: 'REG-01', branch_id: 'b-1', kind: 'safe_drop',
        amount_cents: 5000, reason_code: null, note: '', actor: 'u-1', taken_by: null, gl_entry_id: null,
        created_at: '2026-06-01T10:00:00Z',
    }],
    movement_totals_by_kind: { safe_drop: 5000 },
};

/** A cashier's read of the same open drawer: the blind count carries no money figures. */
const cashierReport: TillReport = {
    ...managerReport,
    session: session() as TillReport['session'],
    sales_total_cents: 0, change_cents: 0,
    tendered_by_method: {}, expected_by_method: {}, refunds_by_method: {},
    movements: [], movement_totals_by_kind: {},
};

/** The frozen close of the drawer, as a stored Z report's payload reads. */
const closedReport: TillReport = {
    ...managerReport,
    session: session({
        status: 'closed', closed_at: '2026-06-01T17:00:00Z', closed_by: 'u-1',
        expected_by_method: { CASH: 21950, CHECK: 4000 },
        counted_by_method: { CASH: 21850, CHECK: 4000 },
        over_short_cents: -100,
    }) as TillReport['session'],
};

describe('the X report panel', () => {
    it('shows the manager the figures of the open drawer from the report', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', { report: managerReport, kind: 'x', branchName: 'Kelowna Main Yard' });
        expect(text(q(el, '[data-testid="till-report-title"]'))).toBe('X REPORT');
        expect(text(el)).toContain('Not a close; the drawer stays open');
        expect(text(q(el, '[data-testid="till-report-register"]'))).toContain('REG-01');
        expect(text(q(el, '[data-testid="till-report-register"]'))).toContain('Kelowna Main Yard');
        expect(text(q(el, '[data-testid="till-report-float"]'))).toBe('$200.00');
        expect(text(q(el, '[data-testid="till-report-sales"]'))).toBe('3 rung - $59.50');
        expect(text(q(el, '[data-testid="till-report-tax"]'))).toBe('$4.50');
        expect(text(q(el, '[data-testid="till-report-tender-cash"]'))).toContain('$19.50');
        expect(text(q(el, '[data-testid="till-report-tender-cash"]'))).toContain('$219.50');
        expect(text(q(el, '[data-testid="till-report-tender-check"]'))).toContain('$40.00');
        expect(text(q(el, '[data-testid="till-report-refund-cash"]'))).toBe('$4.50');
        expect(text(q(el, '[data-testid="till-report-movement"]'))).toContain('Safe drop');
        expect(text(q(el, '[data-testid="till-report-movement"]'))).toContain('$50.00');
        expect(text(q(el, '[data-testid="till-report-expected-cash"]'))).toBe('$219.50');
        expect(el.querySelector('[data-testid="till-report-blind"]')).toBeNull();
        el.remove();
    });

    it('shows the cashier the blind read: no money figures, and says why', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', { report: cashierReport, kind: 'x' });
        expect(text(q(el, '[data-testid="till-report-blind"]'))).toContain('stay blind until the count is posted');
        expect(text(q(el, '[data-testid="till-report-sale-count"]'))).toBe('3');
        expect(text(q(el, '[data-testid="till-report-tax"]'))).toBe('$4.50');
        expect(el.querySelector('[data-testid="till-report-tenders"]')).toBeNull();
        expect(el.querySelector('[data-testid="till-report-float"]')).toBeNull();
        expect(el.querySelector('[data-testid="till-report-expected-cash"]')).toBeNull();
        expect(el.querySelector('[data-testid="till-report-movements"]')).toBeNull();
        el.remove();
    });

    it('prints the sheet through the browser print', async () => {
        const printed = vi.fn();
        vi.stubGlobal('print', printed);
        const el = await mount<TillReportView>('gable-till-report-view', { report: managerReport, kind: 'x' });
        q<HTMLButtonElement>(el, '[data-testid="till-report-print"]').click();
        expect(printed).toHaveBeenCalledOnce();
        const rules = el.querySelector('style')?.textContent ?? '';
        expect(rules).toContain('@media print');
        expect(rules).toContain('.till-report-sheet');
        el.remove();
    });

    it('tells the terminal when it closes', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', { report: managerReport, kind: 'x' });
        const closed = vi.fn();
        el.addEventListener('report-close', closed);
        q<HTMLButtonElement>(el, '[data-testid="till-report-close"]').click();
        expect(closed).toHaveBeenCalledOnce();
        el.remove();
    });
});

const OWN_ID = '9f549213-831c-42d9-a382-64fcf2f9fb83';
const ownDrawer: TillReport = { ...managerReport, session: session({ cashier_id: OWN_ID, expected_by_method: { CASH: 21950 } }) as TillReport['session'] };

describe('the cashier row', () => {
    it('names the signed in person on their own drawer', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', {
            report: ownDrawer, kind: 'x', signedIn: { id: OWN_ID, name: 'Jordan Pike' },
        });
        expect(text(q(el, '[data-testid="till-report-cashier"]'))).toBe('Jordan Pike');
        el.remove();
    });

    it('names the register, never an id, on a drawer the viewer did not open', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', {
            report: ownDrawer, kind: 'z', signedIn: { id: '11111111-1111-4111-8111-111111111111', name: 'Sam Ortiz' },
        });
        const row = text(q(el, '[data-testid="till-report-cashier"]'));
        expect(row).toBe("Register REG-01's cashier");
        expect(row).not.toContain(OWN_ID);
        el.remove();
    });

    it('never prints the nil id a dev stack stamps, nor a name that is itself an id', () => {
        const nil = '00000000-0000-0000-0000-000000000000';
        const devDrawer = { ...ownDrawer, session: { ...ownDrawer.session, cashier_id: nil } } as TillReport;
        expect(cashierLabel(devDrawer, { id: null, name: 'Dev Cashier' })).toBe("Register REG-01's cashier");
        expect(cashierLabel(devDrawer, { id: nil, name: 'Dev Cashier' })).toBe("Register REG-01's cashier");
        expect(cashierLabel(ownDrawer, { id: OWN_ID, name: OWN_ID })).toBe("Register REG-01's cashier");
        expect(cashierLabel(ownDrawer, { id: OWN_ID, name: '  ' })).toBe("Register REG-01's cashier");
    });

    it('reads a report frozen before the refunds figure existed', async () => {
        const old = { ...managerReport } as Partial<TillReport>;
        delete old.refunds_by_method;
        const el = await mount<TillReportView>('gable-till-report-view', { report: old as TillReport, kind: 'z' });
        expect(el.querySelector('[data-testid="till-report-refunds"]')).toBeNull();
        expect(text(q(el, '[data-testid="till-report-expected-cash"]'))).toBe('$219.50');
        el.remove();
    });
});

describe('the Z report read', () => {
    it('shows the frozen close read only, with the count against the expected', async () => {
        const el = await mount<TillReportView>('gable-till-report-view', { report: closedReport, kind: 'z' });
        expect(text(q(el, '[data-testid="till-report-title"]'))).toBe('Z REPORT');
        expect(text(el)).toContain('read only');
        const cash = text(q(el, '[data-testid="till-report-count"]'));
        expect(cash).toContain('$219.50');
        expect(cash).toContain('$218.50');
        expect(cash).toContain('-$1.00');
        el.remove();
    });
});

describe('the report helpers', () => {
    it('reads the blind rule and the drawer cash off the report', () => {
        expect(isBlind(managerReport)).toBe(false);
        expect(isBlind(cashierReport)).toBe(true);
        expect(isBlind(closedReport)).toBe(false);
        expect(expectedDrawerCash(managerReport)).toBe(21950);
        expect(expectedDrawerCash(cashierReport)).toBeNull();
        expect(reportMethods(managerReport)).toEqual(['CASH', 'CHECK']);
    });
});

describe('the Z reports list', () => {
    let urls: string[] = [];

    const stored: ZReport = {
        id: 'z-1', till_session_id: 's-1', register_id: 'REG-01', branch_id: 'b-1',
        over_short_cents: -100, payload: closedReport, generated_at: '2026-06-01T17:00:05Z',
    };

    beforeEach(() => {
        urls = [];
        localStorage.setItem('token', 'test-token');
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            urls.push(String(url));
            if (String(url).includes('/zreports?')) return jsonResponse([stored], 200);
            return jsonResponse(stored, 200);
        }));
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        document.body.innerHTML = '';
    });

    it('lists the register\'s stored Z reports and opens one read only', async () => {
        const el = await mount<TillZReports>('gable-till-z-reports', { registerId: 'REG-01' });
        await flush();
        await update(el, {});
        expect(urls[0]).toContain('/api/v1/pos/zreports?register_id=REG-01');
        const row = q<HTMLButtonElement>(el, '[data-testid="z-report-row"]');
        expect(text(row)).toContain('Short $1.00');
        row.click();
        await flush();
        await update(el, {});
        expect(urls[1]).toContain('/api/v1/pos/till/s-1/zreport');
        const view = q<TillReportView>(el, 'gable-till-report-view');
        expect(text(q(view, '[data-testid="till-report-title"]'))).toBe('Z REPORT');
        // the read is read only: the list offers Back, never an edit
        expect(el.querySelector('input[type="number"]')).toBeNull();
        q<HTMLButtonElement>(el, '[data-testid="z-back"]').click();
        await update(el, {});
        expect(el.querySelector('[data-testid="z-report-row"]')).not.toBeNull();
        el.remove();
    });

    it('has one label on the day field, worded for the yard\'s day', async () => {
        const el = await mount<TillZReports>('gable-till-z-reports', { registerId: 'REG-01' });
        await flush();
        await update(el, {});
        expect(el.querySelectorAll('label').length).toBe(0);
        expect(q(el, '[data-testid="z-day"]').getAttribute('label')).toBe("Business day (blank is the yard's today)");
        el.remove();
    });

    it('says when the day holds none', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse([], 200)));
        const el = await mount<TillZReports>('gable-till-z-reports', { registerId: 'REG-01' });
        await flush();
        await update(el, {});
        expect(text(q(el, '[data-testid="z-empty"]'))).toContain('No Z reports stored');
        el.remove();
    });
});
