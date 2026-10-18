// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { formatCents } from '../../lib/utils.ts';
import { formatDay } from '../../lib/dates.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { branchContext } from '../../services/BranchContext';
import { fetchGLDetail } from '../../services/GLService';
import type { GLDetailRow } from '../../types/gl';
import { X } from 'lucide';

/**
 * The drill down from a statement row: the posted ledger lines of one account
 * over a date window, for one branch or every branch. A statement page puts it
 * under its table when a row is chosen and removes it on `gl-detail-close`.
 */
@customElement('gable-gl-detail')
export class GlDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String, attribute: 'account-id' }) accountId = '';
    @property({ type: String, attribute: 'account-label' }) accountLabel = '';
    @property({ type: String }) from = '';
    @property({ type: String }) to = '';
    /** The branch id, empty for every branch. */
    @property({ type: String, attribute: 'branch-id' }) branchId = '';
    @property({ type: String }) currency = '';

    @state() private rows: GLDetailRow[] = [];
    @state() private loading = true;

    private _key = '';

    willUpdate() {
        const key = [this.accountId, this.from, this.to, this.branchId, this.currency].join('|');
        if (key !== this._key && this.accountId) {
            this._key = key;
            this._load();
        }
    }

    private async _load() {
        this.loading = true;
        try {
            this.rows = await fetchGLDetail(this.accountId, this.from, this.to, this.branchId || undefined, this.currency || undefined);
        } catch (err) {
            console.error(err);
            ToastService.show('Failed to load the ledger lines', 'error');
            this.rows = [];
        } finally {
            this.loading = false;
        }
    }

    private _branchName(id: string): string {
        return branchContext.branches.find(b => b.id === id)?.name ?? id.slice(0, 8);
    }

    render() {
        const debit = this.rows.reduce((s, r) => s + r.debit_cents, 0);
        const credit = this.rows.reduce((s, r) => s + r.credit_cents, 0);
        return html`
            <div class="backdrop-blur-md bg-white/5 border border-emerald-500/30 rounded-xl overflow-hidden">
                <div class="flex justify-between items-center px-4 py-3 border-b border-white/10">
                    <h3 class="text-lg font-semibold text-white">${this.accountLabel}</h3>
                    <button
                        @click=${() => this.dispatchEvent(new CustomEvent('gl-detail-close', { bubbles: true }))}
                        class="text-zinc-400 hover:text-white"
                        title="Close"
                    >${icon(X, 18)}</button>
                </div>
                ${this.loading ? html`
                    <div class="p-6 text-center text-zinc-400">Loading ledger lines...</div>
                ` : this.rows.length === 0 ? html`
                    <div class="p-6 text-center text-zinc-400">No posted lines in this window.</div>
                ` : html`
                    <table class="w-full text-sm">
                        <thead>
                            <tr class="border-b border-white/10 text-zinc-400">
                                <th class="text-left px-4 py-3 font-medium w-24">Entry</th>
                                <th class="text-left px-4 py-3 font-medium w-32">Date</th>
                                <th class="text-left px-4 py-3 font-medium">Memo</th>
                                <th class="text-left px-4 py-3 font-medium w-40">Branch</th>
                                <th class="text-right px-4 py-3 font-medium w-36">Debit</th>
                                <th class="text-right px-4 py-3 font-medium w-36">Credit</th>
                            </tr>
                        </thead>
                        <tbody>
                            ${this.rows.map(r => html`
                                <tr class="border-b border-white/5 hover:bg-white/[0.02] transition-colors">
                                    <td class="px-4 py-2.5 font-mono text-emerald-400">#${r.entry_number}</td>
                                    <td class="px-4 py-2.5 text-zinc-300">${formatDay(r.entry_date)}</td>
                                    <td class="px-4 py-2.5 text-white">${r.description || r.memo}</td>
                                    <td class="px-4 py-2.5 text-zinc-300">${this._branchName(r.branch_id)}</td>
                                    <td class="px-4 py-2.5 text-right font-mono text-zinc-200">${r.debit_cents > 0 ? formatCents(r.debit_cents) : '--'}</td>
                                    <td class="px-4 py-2.5 text-right font-mono text-zinc-200">${r.credit_cents > 0 ? formatCents(r.credit_cents) : '--'}</td>
                                </tr>
                            `)}
                            <tr class="font-bold bg-white/[0.03]">
                                <td colspan="4" class="px-4 py-3 text-right text-white uppercase text-xs tracking-wider">Total</td>
                                <td class="px-4 py-3 text-right font-mono text-zinc-200">${formatCents(debit)}</td>
                                <td class="px-4 py-3 text-right font-mono text-zinc-200">${formatCents(credit)}</td>
                            </tr>
                        </tbody>
                    </table>
                `}
            </div>
        `;
    }
}

export default GlDetail;
