// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The manager's large cash worklist, on the desk (CP-17b): the queue in
// words, the deadline each row carries, the filing the manager records, the
// statement the manager issues, and the row that never offers a filing.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const svc = vi.hoisted(() => ({
    listWorklist: vi.fn(),
    recordFiling: vi.fn(),
    form8300: vi.fn(),
    listStatements: vi.fn(),
    issueStatement: vi.fn(),
}));

vi.mock('../../services/LargeCashService', async (orig) => {
    const real = await orig<typeof import('../../services/LargeCashService')>();
    return { ...real, LargeCashService: svc };
});

import './LargeCashWorklist';
import type { LargeCashWorklist } from './LargeCashWorklist';
import type { LargeCashStatement, LargeCashWorklistRow } from '../../types/largecash';
import { moneyWords, statementLines } from './largeCashWords';

const row = (over: Partial<LargeCashWorklistRow> = {}): LargeCashWorklistRow => ({
    event_id: 'ev1',
    payer_key: 'cus:c1',
    payer_name: 'Riverbend Builders',
    customer_id: 'c1',
    customer_account: 'C-0001',
    branch_id: 'b1',
    branch_name: 'Kelowna Main',
    branch_address: '1 Main Street',
    contributed_branch_ids: ['b1'],
    currency: 'USD',
    total_cents: 1_200_000,
    rule_family: 'US_8300',
    threshold_cents: 1_000_000,
    transaction_count: 1,
    tender_count: 1,
    first_date: '2026-03-02T10:00:00Z',
    last_date: '2026-03-02T10:00:00Z',
    event_state: 'reportable',
    filing_state: 'reportable',
    filing_deadline: '2026-03-17T10:00:00Z',
    past_deadline: false,
    statement_state: 'not_issued',
    payer_details_captured: true,
    severity_score: 365,
    recommended_action: 'file',
    note: '',
    adviser_note: 'The dealer confirms each filing with its own adviser.',
    ...over,
});

const statement = (over: Partial<LargeCashStatement> = {}): LargeCashStatement => ({
    payer_key: 'cus:c1',
    year: 2026,
    payer_name: 'Riverbend Builders',
    payer_address: '12 Mill Road',
    business_name: 'Gable Lumber',
    business_address: '1 Main Street, Kelowna, BC, V1Y 1A1',
    contact_name: 'Kelowna Main',
    telephone: '555-0100',
    aggregate_cents: 1_200_000,
    currency: 'USD',
    crossing_count: 1,
    first_date: '2026-03-02T10:00:00Z',
    last_date: '2026-03-02T10:00:00Z',
    state: 'issued',
    issued_date: '2026-03-20T00:00:00Z',
    issued_by: 'manager',
    due_date: '2027-01-31T00:00:00Z',
    note: 'The information above was furnished to the Internal Revenue Service.',
    adviser_note: 'The dealer confirms each filing with its own adviser.',
    ...over,
});

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const all = (el: Element, id: string) => [...el.querySelectorAll(`[data-testid="${id}"]`)] as HTMLElement[];

async function mount(rows: LargeCashWorklistRow[], statements: LargeCashStatement[] = []): Promise<LargeCashWorklist> {
    svc.listWorklist.mockResolvedValue({ items: rows, count: rows.length, generated_at: '2026-03-02T10:00:00Z' });
    svc.listStatements.mockResolvedValue({ year: 2026, items: statements, count: statements.length });
    const el = document.createElement('gable-large-cash-worklist') as LargeCashWorklist;
    document.body.appendChild(el);
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

describe("the manager's large cash worklist", () => {
    beforeEach(() => { vi.clearAllMocks(); });

    it('lists each crossing with its payer, its amount and its deadline', async () => {
        const el = await mount([row(), row({ event_id: 'ev2', payer_name: 'North Shore Mill', total_cents: 450_000 })]);
        const rows = all(el, 'large-cash-row');
        expect(rows.length).toBe(2);
        expect(rows[0].textContent).toContain('Riverbend Builders');
        expect(rows[0].textContent).toContain('$12,000.00');
        expect(rows[0].textContent).toContain('Kelowna Main');
        expect(q(rows[0], 'large-cash-deadline').textContent).toContain('The filing is due 2026-03-17');
        expect(q(rows[0], 'large-cash-adviser').textContent).toContain('confirms each filing with its own adviser');
        el.remove();
    });

    it('says an empty queue in words', async () => {
        const el = await mount([]);
        expect(q(el, 'large-cash-empty').textContent).toContain('No large cash crossings');
        el.remove();
    });

    it('says a filing past its deadline in words', async () => {
        const el = await mount([row({ past_deadline: true, recommended_action: 'chase_filing' })]);
        const rows = all(el, 'large-cash-row');
        expect(q(rows[0], 'large-cash-deadline').textContent).toContain('is past');
        expect(q(rows[0], 'large-cash-action').textContent).toContain('Chase it');
        el.remove();
    });

    it('offers no filing on an identity declined row, and says why', async () => {
        const el = await mount([row({
            event_state: 'identity declined',
            filing_state: 'not_reportable',
            filing_deadline: null,
            recommended_action: 'manager_review',
            payer_details_captured: false,
            note: 'Identity declined, manager review.',
        })]);
        const rows = all(el, 'large-cash-row');
        expect(q(rows[0], 'large-cash-file')).toBeNull();
        expect(q(rows[0], 'large-cash-note').textContent).toContain('Identity declined, manager review.');
        expect(q(rows[0], 'large-cash-action').textContent).toContain('A manager looks at this one');
        el.remove();
    });

    it('offers no filing on a Canadian crossing, and says it is not a report', async () => {
        const el = await mount([row({
            event_state: 'recorded',
            rule_family: 'CA_DEALER',
            filing_state: 'not_reportable',
            filing_deadline: null,
            recommended_action: 'dealer_record',
            note: "This is for the dealer's own records. It is not a FINTRAC report.",
        })]);
        const rows = all(el, 'large-cash-row');
        expect(q(rows[0], 'large-cash-file')).toBeNull();
        expect(q(rows[0], 'large-cash-note').textContent).toContain('not a FINTRAC report');
        el.remove();
    });

    it('records the filing under the acknowledgement number the manager typed', async () => {
        const el = await mount([row()]);
        svc.recordFiling.mockResolvedValue({ event_id: 'ev1', state: 'filed', recorded_by: 'manager', recorded_at: '2026-03-02T10:00:00Z', past_deadline: false });
        svc.listWorklist.mockResolvedValue({ items: [], count: 0, generated_at: '2026-03-02T10:00:00Z' });
        const ack = el.querySelector('#lc-ack-ev1') as HTMLInputElement;
        ack.value = '31000000000001';
        ack.dispatchEvent(new Event('input'));
        await el.updateComplete;
        q(el, 'large-cash-file').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(svc.recordFiling).toHaveBeenCalledWith('ev1', '31000000000001');
        expect(q(el, 'large-cash-status').textContent).toContain('the Form 8300 for Riverbend Builders is filed');
        el.remove();
    });

    it('refuses a filing with no acknowledgement number, in words', async () => {
        const el = await mount([row()]);
        q(el, 'large-cash-file').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(q(el, 'large-cash-error').textContent).toContain('acknowledgement number');
        expect(svc.recordFiling).not.toHaveBeenCalled();
        el.remove();
    });

    it('issues the customer statement on a filed row', async () => {
        const el = await mount([row({ filing_state: 'filed', recommended_action: 'issue_statement' })]);
        svc.issueStatement.mockResolvedValue(statement());
        svc.listStatements.mockResolvedValue({ year: 2026, items: [statement()], count: 1 });
        svc.listWorklist.mockResolvedValue({ items: [], count: 0, generated_at: '2026-03-02T10:00:00Z' });
        q(el, 'large-cash-statement').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(svc.issueStatement).toHaveBeenCalledWith('ev1');
        expect(q(el, 'large-cash-status').textContent).toContain('the customer statement for Riverbend Builders is out');
        // the issued statement is on the sheet to print and keep
        const sheet = q(el, 'large-cash-statement-view');
        expect(sheet.textContent).toContain('Statement of cash received, 2026');
        expect(sheet.textContent).toContain('Gable Lumber');
        expect(sheet.textContent).toContain('555-0100');
        expect(sheet.textContent).toContain('$12,000.00');
        expect(q(sheet, 'large-cash-statement-note').textContent).toContain('furnished to the Internal Revenue Service');
        el.remove();
    });

    it("lists the year's statements and opens an issued one to print", async () => {
        const print = vi.spyOn(window, 'print').mockImplementation(() => {});
        const el = await mount([], [statement(), statement({ payer_key: 'cus:c2', payer_name: 'North Shore Mill', state: 'not_issued', issued_date: null })]);
        const rows = all(el, 'large-cash-statement-row');
        expect(rows.length).toBe(2);
        expect(rows[1].textContent).toContain('Due 2027-01-31');
        expect(q(rows[1], 'large-cash-statement-open')).toBeNull();
        q(rows[0], 'large-cash-statement-open').click();
        await el.updateComplete;
        expect(q(el, 'large-cash-statement-view').textContent).toContain('Riverbend Builders');
        q(el, 'large-cash-statement-print').click();
        expect(print).toHaveBeenCalledTimes(1);
        q(el, 'large-cash-statement-close').click();
        await el.updateComplete;
        expect(q(el, 'large-cash-statement-view')).toBeNull();
        print.mockRestore();
        el.remove();
    });

    it('prints the dealer as the sender and the payer as the addressee, in the currency, and never a suspicion', () => {
        const lines = statementLines(statement({ currency: 'CAD' }));
        const text = lines.map((l) => `${l.label}: ${l.value}`).join('\n');
        expect(text).toContain('From: Gable Lumber');
        expect(text).toContain('To: Riverbend Builders');
        expect(text).toContain('Payer address: 12 Mill Road');
        expect(text).toContain('Cash received in 2026: CA$12,000.00');
        expect(text.toLowerCase()).not.toContain('suspic');
        expect(moneyWords(450_000, 'USD')).toBe('$4,500.00');
    });

    it('reads the Form 8300 data set on request, and never before', async () => {
        const el = await mount([row()]);
        expect(q(el, 'large-cash-form-data')).toBeNull();
        svc.form8300.mockResolvedValue({
            event_id: 'ev1', form: 'Form 8300', currency: 'USD', amount_cents: 1_200_000,
            transaction_count: 1, tender_count: 1, first_date: '2026-03-02T10:00:00Z',
            last_date: '2026-03-02T10:00:00Z', received_at: '2026-03-02T10:00:00Z',
            filing_deadline: '2026-03-17T10:00:00Z',
            payer: { name: 'Riverbend Builders', address: '12 Mill Road', taxpayer_id: '00-1234567' },
            filer: { branch_id: 'b1', branch_name: 'Kelowna Main', address: '1 Main Street' },
            note: 'Gable does not file this.', adviser_note: 'The dealer confirms each filing with its own adviser.',
        });
        q(el, 'large-cash-form').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(q(el, 'large-cash-form-data').textContent).toContain('Form 8300 for Riverbend Builders');
        expect(q(el, 'large-cash-form-data').textContent).toContain('00-1234567');
        expect(q(el, 'large-cash-form-data').textContent).toContain('Gable does not file this.');
        el.remove();
    });

    it('never shows the taxpayer identification number on the queue itself', async () => {
        const el = await mount([row()]);
        expect(el.textContent).not.toContain('00-1234567');
        el.remove();
    });

    it('says a refusal in words', async () => {
        const el = await mount([row()]);
        svc.recordFiling.mockRejectedValue(new Error('this crossing is already filed'));
        const ack = el.querySelector('#lc-ack-ev1') as HTMLInputElement;
        ack.value = '31000000000001';
        ack.dispatchEvent(new Event('input'));
        await el.updateComplete;
        q(el, 'large-cash-file').click();
        await new Promise((r) => setTimeout(r, 0));
        await el.updateComplete;
        expect(q(el, 'large-cash-error').textContent).toContain('already filed');
        el.remove();
    });
});
