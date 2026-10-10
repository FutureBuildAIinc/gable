// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { ArService } from '../../services/ArService.ts';
import type { ArAgingItem, ArAgingTotal, AgingGroupBy } from '../../types/account.ts';
import { DollarSign } from 'lucide';

type Basis = 'due_date' | 'invoice_date';

const GROUPS: { id: AgingGroupBy; label: string }[] = [
    { id: 'customer', label: 'Customer' },
    { id: 'job', label: 'Job' },
    { id: 'ship_to', label: 'Ship-to' },
];

@customElement('gable-ar-aging-report')
export class ARAgingReportPage extends LitElement {
    createRenderRoot() { return this; }

    @state() private rows: ArAgingItem[] = [];
    @state() private totals: ArAgingTotal[] = [];
    @state() private loading = true;
    @state() private groupBy: AgingGroupBy = 'customer';
    @state() private basis: Basis = 'due_date';
    @state() private asOf = '';

    connectedCallback() {
        super.connectedCallback();
        this._load();
    }

    private async _load() {
        this.loading = true;
        try {
            const [page, summary] = await Promise.all([
                ArService.aging({ groupBy: this.groupBy, basis: this.basis, asOf: this.asOf || undefined }),
                ArService.agingSummary({ basis: this.basis, asOf: this.asOf || undefined })
            ]);
            this.rows = page.items;
            this.totals = summary.totals;
        } catch (err) {
            console.error(err);
            ToastService.show('Failed to load AR aging report', 'error');
        } finally {
            this.loading = false;
        }
    }

    private _setGroup(g: AgingGroupBy) {
        if (g === this.groupBy) return;
        this.groupBy = g;
        this._load();
    }

    private _setBasis(b: Basis) {
        if (b === this.basis) return;
        this.basis = b;
        this._load();
    }

    private _setAsOf(e: InputEvent) {
        this.asOf = (e.target as HTMLInputElement).value;
        this._load();
    }

    /** A row's group label: the customer, the job, or the ship-to code. */
    private _groupLabel(row: ArAgingItem): string {
        switch (this.groupBy) {
            case 'job': return row.job_name || row.job_id?.slice(0, 8) || 'No job';
            case 'ship_to': return row.ship_to_code || row.ship_to_id?.slice(0, 8) || 'No ship-to';
            default: return row.customer_name;
        }
    }

    private _rowSubtitle(row: ArAgingItem): string {
        const customer = row.customer_name;
        if (this.groupBy === 'customer') return row.currency;
        return `${customer} · ${row.currency}`;
    }

    render() {
        if (this.loading) {
            return html`
                <div class="p-12 flex justify-center">
                    <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-gable-green"></div>
                </div>
            `;
        }

        return html`
            <div class="mb-8">
                <h1 class="text-3xl font-bold text-white flex items-center gap-3">
                    ${icon(DollarSign, 32, 'w-8 h-8 text-gable-green')}
                    AR Aging Report
                </h1>
                <p class="text-zinc-500 mt-1">
                    Accounts receivable aging as of ${this.asOf || 'today'}, never adding two currencies
                </p>
            </div>

            <!-- Controls: the group_by (customer, job or ship-to), the basis and the as-of date -->
            <div class="flex flex-wrap items-center gap-3 mb-6" data-testid="aging-controls">
                <div class="flex rounded-lg overflow-hidden border border-white/10" role="group" aria-label="Group by">
                    ${GROUPS.map(g => html`
                        <button
                            class="px-4 py-2 text-sm font-medium transition-colors ${this.groupBy === g.id ? 'bg-gable-green text-white' : 'text-zinc-400 hover:text-white bg-white/5'}"
                            data-testid="group-by-${g.id}"
                            @click=${() => this._setGroup(g.id)}
                        >${g.label}</button>
                    `)}
                </div>
                <div class="flex rounded-lg overflow-hidden border border-white/10" role="group" aria-label="Basis">
                    ${([{ id: 'due_date', label: 'Due date' }, { id: 'invoice_date', label: 'Invoice date' }] as { id: Basis; label: string }[]).map(b => html`
                        <button
                            class="px-4 py-2 text-sm font-medium transition-colors ${this.basis === b.id ? 'bg-white/10 text-white' : 'text-zinc-400 hover:text-white bg-white/5'}"
                            @click=${() => this._setBasis(b.id)}
                        >${b.label}</button>
                    `)}
                </div>
                <label class="flex items-center gap-2 text-sm text-zinc-400">
                    As of
                    <input type="date" .value=${this.asOf} @input=${this._setAsOf}
                        class="bg-zinc-950 border border-zinc-700 rounded px-2 py-1.5 text-zinc-100" />
                </label>
            </div>

            ${this.totals.length > 0 ? html`
                <div class="grid grid-cols-2 md:grid-cols-6 gap-4 mb-6">
                    ${this.totals.flatMap(t => [
                        { label: `Current ${t.currency}`, value: t.current_cents, color: 'text-emerald-400' },
                        { label: `1-30 ${t.currency}`, value: t.days_1_30_cents, color: 'text-amber-400' },
                        { label: `31-60 ${t.currency}`, value: t.days_31_60_cents, color: 'text-orange-400' },
                        { label: `61-90 ${t.currency}`, value: t.days_61_90_cents, color: 'text-orange-400' },
                        { label: `90+ ${t.currency}`, value: t.over_90_cents, color: 'text-rose-400' },
                        { label: `Total ${t.currency}`, value: t.total_cents, color: 'text-white' },
                    ]).map((item) => html`
                        <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl">
                            <div class="p-4 text-center">
                                <p class="text-xs text-zinc-500 uppercase tracking-wider mb-1">${item.label}</p>
                                <p class="text-xl font-mono font-bold ${item.color}">${formatCents(item.value)}</p>
                            </div>
                        </div>
                    `)}
                </div>
            ` : nothing}

            <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl" data-testid="aging-table">
                <div class="p-0">
                    ${this.rows.length === 0 ? html`
                        <div class="p-12 text-center text-zinc-500">No outstanding receivables</div>
                    ` : html`
                        <table class="w-full text-sm text-left">
                            <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                <tr>
                                    <th class="px-6 py-4">${GROUPS.find(g => g.id === this.groupBy)?.label}</th>
                                    <th class="px-6 py-4 text-right">Current</th>
                                    <th class="px-6 py-4 text-right">1-30</th>
                                    <th class="px-6 py-4 text-right">31-60</th>
                                    <th class="px-6 py-4 text-right">61-90</th>
                                    <th class="px-6 py-4 text-right">90+</th>
                                    <th class="px-6 py-4 text-right">Unapplied</th>
                                    <th class="px-6 py-4 text-right">Total</th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-white/5">
                                ${this.rows.map((row) => html`
                                    <tr class="hover:bg-white/5 transition-colors" data-testid="aging-row">
                                        <td class="px-6 py-4 text-white font-medium">
                                            ${this._groupLabel(row)}
                                            <span class="text-zinc-500 text-xs ml-2">${this._rowSubtitle(row)}</span>
                                        </td>
                                        <td class="px-6 py-4 text-right font-mono text-emerald-400">${formatCents(row.current_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-amber-400">${formatCents(row.days_1_30_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-orange-400">${formatCents(row.days_31_60_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-orange-400">${formatCents(row.days_61_90_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-rose-400">${formatCents(row.over_90_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-teal-300">${formatCents(row.unapplied_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-white font-bold">${formatCents(row.total_cents)}</td>
                                    </tr>
                                `)}
                            </tbody>
                            <tfoot class="bg-white/5 border-t border-white/10">
                                ${this.totals.map(t => html`
                                    <tr class="font-bold">
                                        <td class="px-6 py-4 text-zinc-400 uppercase text-xs">Totals ${t.currency}</td>
                                        <td class="px-6 py-4 text-right font-mono text-emerald-400">${formatCents(t.current_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-amber-400">${formatCents(t.days_1_30_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-orange-400">${formatCents(t.days_31_60_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-orange-400">${formatCents(t.days_61_90_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-rose-400">${formatCents(t.over_90_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-teal-300">${formatCents(t.unapplied_cents)}</td>
                                        <td class="px-6 py-4 text-right font-mono text-white">${formatCents(t.total_cents)}</td>
                                    </tr>
                                `)}
                            </tfoot>
                        </table>
                    `}
                </div>
            </div>
        `;
    }
}
