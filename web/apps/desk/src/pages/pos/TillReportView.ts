// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import type { TillReport } from '../../types/pos';
import { formatDateTime } from '../../lib/dates.ts';
import { kindLabel } from './movements.ts';
import { cashierLabel, expectedDrawerCash, isBlind, reportMethodLabel, reportMethods, signedInPerson, type SignedInPerson, type TillReportKind } from './tillReport.ts';

const fmt = (cents: number) => (cents < 0 ? `-$${(-cents / 100).toFixed(2)}` : `$${(cents / 100).toFixed(2)}`);

const MUTED = 'color:rgb(var(--gb-till-muted))';
const RULE = 'border-top:1px solid rgb(var(--gb-till-rule))';

/**
 * One till report on screen and on paper (CP-08): the live X read of an open
 * drawer, or a frozen Z read of a closed one. What shows is what the report
 * carries: the close is a blind count, so a cashier's report of an open
 * drawer arrives without the money figures and the panel says so, while a
 * manager's carries the taken, expected and refunds figures and the expected
 * drawer cash. The sheet prints through the browser with the print rules
 * below: only the sheet shows, black on white, headed X REPORT or Z REPORT.
 */
@customElement('gable-till-report-view')
export class TillReportView extends LitElement {
    createRenderRoot() { return this; }

    /** The report to show; null while the caller is still reading it. */
    @property({ attribute: false }) report: TillReport | null = null;
    /** x: the live mid-shift read; z: the frozen close. */
    @property() kind: TillReportKind = 'x';
    /** The register's branch name, when the caller knows it. */
    @property({ attribute: 'branch-name' }) branchName = '';
    /** Who is signed in, to name their own drawer's cashier; read from the session when not given. */
    @property({ attribute: false }) signedIn: SignedInPerson | null = null;

    private close() {
        this.dispatchEvent(new CustomEvent('report-close', { bubbles: true }));
    }

    private print() {
        window.print();
    }

    private row(label: string, value: unknown, testid?: string) {
        return html`
            <div style="display:flex;justify-content:space-between;gap:10px;padding:6px 0;${RULE};font-size:13px">
                <span style=${MUTED}>${label}</span>
                <span style="font-weight:600;text-align:right" data-testid=${testid ?? nothing}>${value}</span>
            </div>`;
    }

    /** The tender table: taken and expected per method, expected only where the report carries it. */
    private tendersTable(r: TillReport) {
        const methods = reportMethods(r);
        const withExpected = !isBlind(r);
        if (methods.length === 0 && !withExpected) return nothing;
        return html`
            <table style="width:100%;border-collapse:collapse;font-size:13px;margin-top:6px" data-testid="till-report-tenders">
                <thead><tr style="${MUTED};text-align:right">
                    <th style="text-align:left;padding:4px 0">Tender</th><th>Taken</th>
                    ${withExpected ? html`<th>Expected</th>` : nothing}
                </tr></thead>
                <tbody>
                    ${methods.map((m) => html`
                        <tr style="text-align:right;${RULE}" data-testid="till-report-tender-${m.toLowerCase()}">
                            <td style="text-align:left;padding:6px 0">${reportMethodLabel(m)}</td>
                            <td class="font-mono">${fmt(r.tendered_by_method?.[m] ?? 0)}</td>
                            ${withExpected ? html`<td class="font-mono">${fmt(r.expected_by_method?.[m] ?? 0)}</td>` : nothing}
                        </tr>`)}
                </tbody>
            </table>`;
    }

    private movementsList(r: TillReport) {
        const movements = r.movements ?? [];
        if (movements.length === 0) return nothing;
        return html`
            <div style="margin-top:14px" data-testid="till-report-movements">
                <div style="font-size:12px;${MUTED};text-transform:uppercase;letter-spacing:0.5px;margin-bottom:4px">Drawer cash movements</div>
                ${movements.map((m) => html`
                    <div style="display:flex;justify-content:space-between;gap:10px;padding:6px 0;${RULE};font-size:13px" data-testid="till-report-movement">
                        <span>${kindLabel(m.kind)}${m.note ? html` <span style=${MUTED}>- ${m.note}</span>` : nothing}</span>
                        <span class="font-mono" style="font-weight:600">${fmt(m.amount_cents)}</span>
                    </div>`)}
            </div>`;
    }

    /** The counted against the expected with the variance, once the drawer is counted (the Z read). */
    private countTable(r: TillReport) {
        const counted = r.session.counted_by_method ?? {};
        const expected = r.session.expected_by_method ?? {};
        const methods = Object.keys(expected).filter((m) => m.toLowerCase() !== 'account').sort();
        if (methods.length === 0) return nothing;
        return html`
            <table style="width:100%;border-collapse:collapse;font-size:13px;margin-top:6px" data-testid="till-report-count">
                <thead><tr style="${MUTED};text-align:right">
                    <th style="text-align:left;padding:4px 0">Method</th><th>Expected</th><th>Counted</th><th>Over / short</th>
                </tr></thead>
                <tbody>
                    ${methods.map((m) => {
                        const d = (counted[m] ?? 0) - (expected[m] ?? 0);
                        const color = d === 0 ? 'rgb(var(--gb-till-muted))' : d < 0 ? 'rgb(var(--gb-till-bad))' : 'rgb(var(--gb-till-warn))';
                        return html`<tr style="text-align:right;${RULE}">
                            <td style="text-align:left;padding:6px 0">${reportMethodLabel(m)}</td>
                            <td class="font-mono">${fmt(expected[m] ?? 0)}</td>
                            <td class="font-mono">${fmt(counted[m] ?? 0)}</td>
                            <td class="font-mono" style="color:${color}">${fmt(d)}</td>
                        </tr>`;
                    })}
                </tbody>
            </table>`;
    }

    render() {
        const r = this.report;
        if (!r) return nothing;
        const blind = isBlind(r);
        const isX = this.kind === 'x';
        const refundsBy = r.refunds_by_method ?? {};
        const refunds = Object.keys(refundsBy).sort();
        const drawerCash = expectedDrawerCash(r);
        return html`
            <style>
                @media print {
                    body { background: #fff !important; }
                    body * { visibility: hidden; }
                    .till-report-sheet, .till-report-sheet * { visibility: visible; }
                    .till-report-sheet { position: absolute; left: 0; top: 0; width: 100%; padding: 12px; background: #fff !important; }
                    .till-report-sheet, .till-report-sheet * { color: #000 !important; border-color: #666 !important; }
                    .till-report-actions { display: none !important; }
                }
            </style>
            <div class="till-report-sheet" data-testid=${isX ? 'till-x-report' : 'till-z-report'}>
                <h2 style="margin:0 0 4px;font-size:18px;font-weight:800;letter-spacing:1px" data-testid="till-report-title">${isX ? 'X REPORT' : 'Z REPORT'}</h2>
                <p style="margin:0 0 14px;font-size:13px;${MUTED}">
                    ${isX
                        ? 'Not a close; the drawer stays open. A mid-shift read of where the drawer stands.'
                        : 'The frozen close of this drawer, read only.'}
                </p>
                ${this.row('Register', html`<span class="font-mono">${r.session.register_id}</span>${this.branchName ? html` - ${this.branchName}` : nothing}`, 'till-report-register')}
                ${this.row('Cashier', cashierLabel(r, this.signedIn ?? signedInPerson()), 'till-report-cashier')}
                ${this.row('Opened', formatDateTime(r.session.opened_at), 'till-report-opened')}
                ${blind ? html`
                    <p style="margin:14px 0 0;font-size:13px;color:rgb(var(--gb-till-warn))" data-testid="till-report-blind">
                        The drawer's money figures stay blind until the count is posted; a manager's X report shows them.
                    </p>
                    ${this.row('Sales rung', r.sale_count, 'till-report-sale-count')}
                    ${this.row('Tax taken', fmt(r.tax_total_cents), 'till-report-tax')}
                ` : html`
                    ${this.row('Opening float', fmt(r.session.opening_float_cents), 'till-report-float')}
                    ${this.row('Sales', `${r.sale_count} rung - ${fmt(r.sales_total_cents)}`, 'till-report-sales')}
                    ${this.row('Tax taken', fmt(r.tax_total_cents), 'till-report-tax')}
                    ${this.tendersTable(r)}
                    ${refunds.length > 0 ? html`
                        <div style="margin-top:14px" data-testid="till-report-refunds">
                            <div style="font-size:12px;${MUTED};text-transform:uppercase;letter-spacing:0.5px;margin-bottom:4px">Refunds</div>
                            ${refunds.map((m) => this.row(reportMethodLabel(m), html`<span class="font-mono">${fmt(refundsBy[m])}</span>`, `till-report-refund-${m.toLowerCase()}`))}
                        </div>` : nothing}
                    ${this.movementsList(r)}
                    ${drawerCash !== null ? html`
                        <div style="text-align:center;padding:12px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:10px;margin-top:14px">
                            <div style="font-size:12px;${MUTED};text-transform:uppercase;letter-spacing:0.5px">Expected drawer cash</div>
                            <div class="font-mono" style="font-size:26px;font-weight:800" data-testid="till-report-expected-cash">${fmt(drawerCash)}</div>
                        </div>` : nothing}
                    ${!isX ? this.countTable(r) : nothing}
                `}
            </div>
            <div class="till-report-actions" style="display:flex;gap:8px;margin-top:18px">
                <button type="button" data-testid="till-report-close" @click=${() => this.close()}
                    style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Close</button>
                <button type="button" data-testid="till-report-print" @click=${() => this.print()}
                    style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">Print</button>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-till-report-view': TillReportView;
    }
}
