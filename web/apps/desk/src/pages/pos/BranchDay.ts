// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import '../../components/ui/gable-date-field.ts';
import type { GableDateField } from '../../components/ui/gable-date-field.ts';
import '../../components/ui/gable-select.ts';
import { posService } from '../../services/POSService.ts';
import { listBankAccounts } from '../../services/BankReconService.ts';
import type { BankAccount } from '../../types/bankrecon.ts';
import { ApiError } from '../../services/apiError.ts';
import { branchContext } from '../../services/BranchContext.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { formatDay, formatDateTime } from '../../lib/dates.ts';
import type { BranchDay, DepositCandidate } from '../../types/pos.ts';

const cashOf = (m: Record<string, number> | undefined) => (m && typeof m.CASH === 'number' ? formatCents(m.CASH) : '-');
const signed = (cents: number) => (cents > 0 ? `+${formatCents(cents)}` : formatCents(cents));

/**
 * The branch's end of day (CP-09), on the Daily Till page: each register's
 * sessions for a business date in the branch's zone, the figures to bank, the
 * close with its deposit slip reference, and the match of the deposit to a bank
 * statement line. The close waits for every drawer to be counted and, where a
 * count passed the variance threshold, signed off.
 */
@customElement('gable-branch-day')
export class GableBranchDay extends LitElement {
    createRenderRoot() { return this; }

    @state() private day: BranchDay | null = null;
    @state() private date = '';
    @state() private loading = true;
    @state() private failure = '';
    /** The server refuses the end of day to this user: the card does not show. */
    @state() private forbidden = false;
    @state() private slip = '';
    /** The bank accounts the deposit may go to, and the one chosen. */
    @state() private banks: BankAccount[] = [];
    @state() private bank = '';
    @state() private busy = false;
    @state() private candidates: DepositCandidate[] | null = null;
    @state() private chosen = '';

    private unsubscribe: (() => void) | null = null;
    /** Counts loads; an answer to an older one is dropped. */
    private seq = 0;

    connectedCallback() {
        super.connectedCallback();
        this.unsubscribe = onBranchChanged(() => { this.date = ''; void this.load(); });
        void this.load();
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        this.unsubscribe?.();
        this.seq++;
    }

    private async load() {
        const id = branchContext.currentId;
        const seq = ++this.seq;
        this.failure = '';
        this.forbidden = false;
        this.candidates = null;
        this.chosen = '';
        if (!id) {
            this.day = null;
            this.loading = false;
            return;
        }
        this.loading = true;
        try {
            const day = await posService.branchDay(id, this.date || undefined);
            if (seq !== this.seq) return;
            this.day = day;
            this.date = day.business_date;
            if (this.banks.length === 0) {
                // the deposit goes to a bank account; a list that cannot be read leaves the choice empty
                try {
                    const banks = await listBankAccounts();
                    if (seq === this.seq) {
                        this.banks = banks.filter((b) => b.is_active !== false);
                        if (!this.bank && this.banks.length === 1) this.bank = this.banks[0].id;
                    }
                } catch {
                    this.banks = [];
                }
            }
        } catch (err: unknown) {
            if (seq !== this.seq) return;
            this.day = null;
            this.forbidden = err instanceof ApiError && err.status === 403;
            this.failure = err instanceof Error ? err.message : 'Failed to load the branch day';
        } finally {
            if (seq === this.seq) this.loading = false;
        }
    }

    private async closeDay() {
        const day = this.day;
        if (!day) return;
        try {
            this.busy = true;
            this.failure = '';
            this.day = await posService.closeBranchDay(day.branch_id, day.business_date, this.slip.trim(), this.bank || undefined);
            this.slip = '';
            ToastService.show(`${formatDay(day.business_date)} closed for the branch`, 'success');
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to close the branch day';
        } finally {
            this.busy = false;
        }
    }

    private async findLines() {
        const close = this.day?.close;
        if (!close) return;
        try {
            this.busy = true;
            this.failure = '';
            this.candidates = await posService.depositCandidates(close.id);
            this.chosen = this.candidates[0]?.bank_transaction_id ?? '';
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to load the bank statement lines';
        } finally {
            this.busy = false;
        }
    }

    private async match() {
        const day = this.day;
        const close = day?.close;
        if (!day || !close || !this.chosen) return;
        try {
            this.busy = true;
            this.failure = '';
            const matched = await posService.matchDeposit(close.id, this.chosen);
            this.day = { ...day, close: matched };
            this.candidates = null;
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to match the deposit';
        } finally {
            this.busy = false;
        }
    }

    private figure(label: string, value: string, testid: string) {
        return html`
            <div class="p-4 bg-black/20 rounded border border-white/5">
                <span class="block text-zinc-400 text-sm uppercase font-bold mb-1">${label}</span>
                <span class="block text-2xl font-mono text-white" data-testid=${testid}>${value}</span>
            </div>`;
    }

    private renderClose(day: BranchDay) {
        const close = day.close;
        if (close) {
            const m = close.deposit_match;
            return html`
                <div class="mt-6 pt-6 border-t border-zinc-800 space-y-4" data-testid="day-closed">
                    <p class="text-zinc-300">
                        Closed by <span class="font-mono text-white">${close.closed_by || 'unknown'}</span>
                        on ${formatDateTime(close.closed_at)}, deposit slip
                        <span class="font-mono text-white" data-testid="day-slip">${close.deposit_slip_ref}</span>.
                    </p>
                    ${m ? html`
                        <div class="p-4 bg-black/20 rounded border border-white/5" data-testid="deposit-matched">
                            <p class="text-zinc-300">
                                Matched to the bank line of ${formatDay(m.bank_transaction_date)}
                                ${m.bank_description ? html`(${m.bank_description})` : nothing}:
                                <span class="font-mono text-white">${formatCents(m.bank_amount_cents)}</span>
                            </p>
                            <p class="mt-1 text-sm ${m.difference_cents === 0 ? 'text-emerald-400' : 'text-amber-500'}" data-testid="deposit-difference">
                                ${m.difference_cents === 0 ? 'The bank and the slip agree.' : `Difference ${signed(m.difference_cents)} against the slip.`}
                                <span class="text-zinc-500">Matched by ${m.matched_by || 'unknown'}.</span>
                            </p>
                        </div>` : this.candidates === null ? html`
                        <button data-testid="deposit-find" @click=${() => this.findLines()} ?disabled=${this.busy}
                            class="px-4 py-2 rounded border border-zinc-700 text-zinc-200 text-sm font-bold">Match the deposit to the bank</button>` : this.candidates.length === 0 ? html`
                        <p class="text-zinc-500 text-sm" data-testid="deposit-none">No unmatched deposit lines on the bank statement near this day.</p>` : html`
                        <div class="space-y-2" data-testid="deposit-candidates">
                            ${this.candidates.map((c) => html`
                                <label class="flex items-center justify-between gap-3 p-3 bg-black/20 rounded border border-white/5 cursor-pointer">
                                    <span class="flex items-center gap-3 text-zinc-300">
                                        <input type="radio" name="deposit-line" .checked=${this.chosen === c.bank_transaction_id}
                                            @change=${() => { this.chosen = c.bank_transaction_id; }} />
                                        ${formatDay(c.transaction_date)} ${c.description}
                                    </span>
                                    <span class="font-mono text-white">${formatCents(c.amount_cents)}</span>
                                </label>`)}
                            <button data-testid="deposit-match" @click=${() => this.match()} ?disabled=${this.busy || !this.chosen}
                                class="px-4 py-2 rounded bg-emerald-500 text-black text-sm font-bold">Match this line</button>
                        </div>`}
                </div>`;
        }
        const blocked = day.open_session_count > 0 || day.pending_signoff_count > 0;
        // cash and checks to deposit need a bank account to go to
        const needsBank = day.totals.cash_to_deposit_cents + day.totals.checks_to_deposit_cents > 0;
        return html`
            <div class="mt-6 pt-6 border-t border-zinc-800 space-y-4">
                ${blocked ? html`
                    <p class="text-amber-500 text-sm" data-testid="day-blocked">
                        ${day.open_session_count > 0 ? `${day.open_session_count} drawer${day.open_session_count === 1 ? ' is' : 's are'} still open. ` : ''}${day.pending_signoff_count > 0 ? `${day.pending_signoff_count} counted drawer${day.pending_signoff_count === 1 ? ' waits' : 's wait'} for a manager's sign off. ` : ''}The day closes when every drawer is counted and settled.
                    </p>` : nothing}
                <div class="flex flex-wrap items-end gap-3">
                    <label class="block">
                        <span class="block text-zinc-400 text-sm uppercase font-bold mb-1">Deposit slip reference</span>
                        <input type="text" data-testid="day-slip-input" .value=${this.slip} maxlength="64"
                            @input=${(e: Event) => { this.slip = (e.target as HTMLInputElement).value; }}
                            class="px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white font-mono" />
                    </label>
                    ${needsBank ? html`
                        <label class="block min-w-[14rem]">
                            <span class="block text-zinc-400 text-sm uppercase font-bold mb-1">Deposit to</span>
                            <gable-select data-testid="day-bank" label="Deposit to" placeholder="Choose the bank account"
                                .value=${this.bank} .options=${this.banks.map((b) => ({ value: b.id, label: b.name }))}
                                @change=${(e: Event) => { this.bank = (e.target as HTMLSelectElement).value; }}></gable-select>
                        </label>` : nothing}
                    <button data-testid="day-close" @click=${() => this.closeDay()} ?disabled=${this.busy || blocked || this.slip.trim() === '' || (needsBank && this.bank === '')}
                        class="px-4 py-2 rounded bg-emerald-500 text-black text-sm font-bold disabled:opacity-40">Close the day</button>
                </div>
            </div>`;
    }

    render() {
        if (this.forbidden) return nothing;
        if (!branchContext.currentId && !this.loading) {
            return html`<div class="bg-zinc-900 border border-zinc-800 rounded-lg p-6 text-zinc-500" data-testid="branch-day-none">Choose a yard to see its end of day.</div>`;
        }
        if (this.loading && !this.day) {
            return html`<div class="bg-zinc-900 border border-zinc-800 rounded-lg p-6 text-zinc-400">Loading the branch day...</div>`;
        }
        const day = this.day;
        if (!day) {
            return html`<div class="bg-zinc-900 border border-zinc-800 rounded-lg p-6 text-red-400" role="alert" data-testid="branch-day-failed">${this.failure || 'The branch day could not be loaded.'}</div>`;
        }
        const t = day.totals;
        return html`
            <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-6" data-testid="branch-day">
                <div class="flex items-center justify-between mb-6 gap-4">
                    <h2 class="text-xl font-bold text-emerald-400">End of Day (${formatDay(day.business_date)})</h2>
                    <gable-date-field label="Business day" data-testid="day-date" .value=${this.date}
                        @change=${(e: Event) => {
                            const picked = (e.target as GableDateField).value;
                            if (picked && picked !== this.date) { this.date = picked; void this.load(); }
                        }}></gable-date-field>
                </div>

                ${day.registers.length === 0 ? html`
                    <p class="text-zinc-500" data-testid="day-empty">No drawer was opened at this branch on this day.</p>` : html`
                    <table class="w-full text-sm" data-testid="day-registers">
                        <thead>
                            <tr class="text-zinc-400 uppercase text-xs text-right">
                                <th class="text-left py-2">Register</th><th>Drawers</th><th>Sales</th><th>Expected cash</th><th>Counted cash</th><th>Over / short</th>
                            </tr>
                        </thead>
                        <tbody class="font-mono text-white">
                            ${day.registers.map((r) => html`
                                <tr class="text-right border-t border-zinc-800" data-testid="day-register-${r.register_id}">
                                    <td class="text-left py-2">${r.register_id}</td>
                                    <td>${r.session_count}</td>
                                    <td>${formatCents(r.sales_total_cents)}</td>
                                    <td>${cashOf(r.expected_by_method)}</td>
                                    <td>${cashOf(r.counted_by_method)}</td>
                                    <td class=${r.over_short_cents === 0 ? '' : r.over_short_cents < 0 ? 'text-red-400' : 'text-amber-500'}>${signed(r.over_short_cents)}</td>
                                </tr>`)}
                            <tr class="text-right border-t border-zinc-700 font-bold" data-testid="day-totals">
                                <td class="text-left py-2">Branch</td>
                                <td>${t.session_count}</td>
                                <td>${formatCents(t.sales_total_cents)}</td>
                                <td>${cashOf(t.expected_by_method)}</td>
                                <td>${cashOf(t.counted_by_method)}</td>
                                <td class=${t.over_short_cents === 0 ? '' : t.over_short_cents < 0 ? 'text-red-400' : 'text-amber-500'}>${signed(t.over_short_cents)}</td>
                            </tr>
                        </tbody>
                    </table>`}

                <div class="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-6">
                    ${this.figure('Cash to deposit', formatCents(t.cash_to_deposit_cents), 'day-cash')}
                    ${this.figure('Checks to deposit', formatCents(t.checks_to_deposit_cents), 'day-checks')}
                    ${this.figure('Card total', formatCents(t.card_total_cents), 'day-card')}
                </div>

                ${this.failure ? html`<p class="mt-4 text-red-400 text-sm" role="alert" data-testid="branch-day-error">${this.failure}</p>` : nothing}
                ${this.renderClose(day)}
            </div>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-branch-day': GableBranchDay;
    }
}
