// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { ArService } from '../../services/ArService.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { LedgerOutsideYardError, ledgerYardNote } from '../../lib/ledgerYards.ts';
import type { ArStatement, ArStatementCurrency } from '../../types/account.ts';
import type { Customer } from '../../types/customer.ts';
import { FileText, Search } from 'lucide';

@customElement('gable-customer-statement')
export class CustomerStatementPage extends LitElement {
    createRenderRoot() { return this; }

    @state() private customerId = '';
    /** The picked customer: null until the user picks one (or the URL preselects). */
    @state() private selectedCustomer: Customer | null = null;
    @state() private searchTerm = '';
    @state() private searchMatches: Customer[] = [];
    @state() private searchOpen = false;
    @state() private startDate = '';
    @state() private endDate = '';
    @state() private statement: ArStatement | null = null;
    @state() private loading = false;
    /** Set when the statement answers 404 for a customer whose ledger is kept at another yard. */
    @state() private yardNote = '';
    private _searchDebounce: ReturnType<typeof setTimeout> | null = null;

    connectedCallback() {
        super.connectedCallback();
        // The Account page's "Statement" action lands here with `?customer=<id>`.
        // Preselect that customer and run the statement so the user sees something
        // useful before they have to type or click anything.
        const url = new URL(window.location.href);
        const fromQuery = url.searchParams.get('customer');
        if (fromQuery && !this.customerId) {
            void this._preselectById(fromQuery);
        }
    }

    private async _preselectById(id: string) {
        this.loading = true;
        try {
            const cust = await CustomerService.getCustomer(id);
            this.selectedCustomer = cust;
            this.customerId = cust.id;
            this.searchTerm = cust.name;
            await this._loadStatement();
        } catch (err) {
            console.error(err);
            ToastService.show('Failed to load the customer for this statement', 'error');
        } finally {
            this.loading = false;
        }
    }

    private _onSearchInput(e: Event) {
        const term = (e.target as HTMLInputElement).value;
        this.searchTerm = term;
        // A user typing means they want a fresh search; clear any prior pick so
        // Generate never fires for a customer the user has decided to replace.
        this.selectedCustomer = null;
        this.customerId = '';
        this.searchOpen = true;
        if (this._searchDebounce) clearTimeout(this._searchDebounce);
        this._searchDebounce = setTimeout(() => this._runSearch(term), 200);
    }

    private async _runSearch(term: string) {
        const query = term.trim();
        if (query.length === 0) {
            this.searchMatches = [];
            return;
        }
        try {
            const page = await CustomerService.listCustomers({ q: query, limit: 10 });
            this.searchMatches = page.items;
        } catch (err) {
            console.error(err);
            this.searchMatches = [];
        }
    }

    private _pickCustomer(c: Customer) {
        this.selectedCustomer = c;
        this.customerId = c.id;
        this.searchTerm = c.name;
        this.searchMatches = [];
        this.searchOpen = false;
    }

    private async _loadStatement() {
        if (!this.customerId.trim()) {
            ToastService.show('Pick a customer first', 'error');
            return;
        }
        this.loading = true;
        this.yardNote = '';
        try {
            this.statement = await ArService.statement(this.customerId.trim(), this.startDate || undefined, this.endDate || undefined);
        } catch (err) {
            if (err instanceof LedgerOutsideYardError) {
                this.statement = null;
                this.yardNote = ledgerYardNote(this.selectedCustomer?.branch_ids ?? []);
                return;
            }
            console.error(err);
            ToastService.show('Failed to load statement', 'error');
        } finally {
            this.loading = false;
        }
    }

    render() {
        return html`
            <div class="mb-8">
                <h1 class="text-3xl font-bold text-white flex items-center gap-3">
                    ${icon(FileText, 32, 'w-8 h-8 text-gable-green')}
                    Customer Statement
                </h1>
                <p class="text-zinc-500 mt-1">Generate account activity statements</p>
            </div>

            <!-- Search Controls -->
            <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl mb-6 no-print">
                <div class="p-6">
                    <div class="flex flex-wrap gap-4 items-end">
                        <div class="flex-1 min-w-[260px] relative">
                            <label class="text-xs text-zinc-500 uppercase tracking-wider block mb-1" for="statement-customer-search">Customer</label>
                            <input
                                id="statement-customer-search"
                                type="text"
                                autocomplete="off"
                                .value=${this.searchTerm}
                                @input=${this._onSearchInput}
                                @blur=${() => setTimeout(() => { this.searchOpen = false; }, 150)}
                                @focus=${() => { if (this.searchMatches.length) this.searchOpen = true; }}
                                placeholder="Search by name or account number"
                                class="w-full bg-black/20 border border-white/10 rounded px-3 py-2 text-white focus:border-[#00FFA3] outline-none"
                                data-testid="customer-search"
                            />
                            ${this.selectedCustomer ? html`
                                <div class="mt-1 text-xs text-zinc-400" data-testid="customer-picked">
                                    <span class="text-white">${this.selectedCustomer.name}</span>
                                    <span class="font-mono ml-2">${this.selectedCustomer.account_number}</span>
                                </div>
                            ` : nothing}
                            ${this.searchOpen && this.searchMatches.length > 0 ? html`
                                <ul class="absolute z-20 mt-1 w-full max-h-60 overflow-auto bg-zinc-900 border border-white/10 rounded shadow-xl" data-testid="customer-matches">
                                    ${this.searchMatches.map(c => html`
                                        <li>
                                            <button
                                                type="button"
                                                class="w-full text-left px-3 py-2 hover:bg-white/10 transition-colors"
                                                data-testid="customer-match"
                                                @mousedown=${(e: Event) => e.preventDefault()}
                                                @click=${() => this._pickCustomer(c)}
                                            >
                                                <span class="text-white">${c.name}</span>
                                                <span class="text-zinc-400 font-mono text-xs ml-2">${c.account_number}</span>
                                            </button>
                                        </li>
                                    `)}
                                </ul>
                            ` : nothing}
                            ${this.searchOpen && this.searchTerm.trim().length > 0 && this.searchMatches.length === 0 ? html`
                                <div class="absolute z-20 mt-1 w-full bg-zinc-900 border border-white/10 rounded shadow-xl p-3 text-zinc-500 text-sm" data-testid="customer-no-matches">
                                    No customers match.
                                </div>
                            ` : nothing}
                        </div>
                        <div>
                            <label class="text-xs text-zinc-500 uppercase tracking-wider block mb-1">From</label>
                            <input
                                type="date"
                                .value=${this.startDate}
                                @input=${(e: Event) => this.startDate = (e.target as HTMLInputElement).value}
                                class="bg-black/20 border border-white/10 rounded px-3 py-2 text-white focus:border-[#00FFA3] outline-none"
                            />
                        </div>
                        <div>
                            <label class="text-xs text-zinc-500 uppercase tracking-wider block mb-1">To</label>
                            <input
                                type="date"
                                .value=${this.endDate}
                                @input=${(e: Event) => this.endDate = (e.target as HTMLInputElement).value}
                                class="bg-black/20 border border-white/10 rounded px-3 py-2 text-white focus:border-[#00FFA3] outline-none"
                            />
                        </div>
                        <button
                            @click=${this._loadStatement}
                            ?disabled=${this.loading || !this.selectedCustomer}
                            class="inline-flex items-center gap-2 bg-[#00FFA3] text-black font-semibold px-4 py-2 rounded hover:shadow-[0_0_10px_rgba(0,255,163,0.3)] transition-all disabled:opacity-50"
                            data-testid="generate-statement"
                        >
                            ${icon(Search, 16, 'w-4 h-4')}
                            Generate
                        </button>
                        ${this.statement ? html`
                            <button
                                @click=${() => window.print()}
                                class="inline-flex items-center gap-2 border border-white/10 text-white px-4 py-2 rounded hover:bg-white/5 transition-colors"
                            >
                                ${icon(FileText, 16, 'w-4 h-4')}
                                Print
                            </button>
                        ` : nothing}
                    </div>
                </div>
            </div>

            ${this.yardNote ? html`<div class="border border-white/5 rounded-lg bg-slate-steel/20 px-4 py-8 text-center text-zinc-400" data-testid="statement-yard-note">${this.yardNote}</div>` : nothing}
            ${this.statement ? this.statement.currencies.map(cur => this._renderCurrency(cur)) : nothing}
        `;
    }

    /** One currency's statement: never mixed with another's amounts. */
    private _renderCurrency(cur: ArStatementCurrency) {
        const st = this.statement!;
        return html`
            <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl mb-6" data-testid="statement-currency">
                <div class="p-6">
                    <div class="flex justify-between items-start mb-6">
                        <div>
                            <h2 class="text-xl font-bold text-white">${st.customer_name}</h2>
                            <p class="text-sm text-zinc-400">
                                Period: ${st.from} to ${st.to} · ${cur.currency}
                            </p>
                        </div>
                        <div class="text-right">
                            <p class="text-xs text-zinc-500">Opening Balance</p>
                            <p class="text-lg font-mono font-bold text-white">${formatCents(cur.opening_balance_cents)}</p>
                        </div>
                    </div>

                    <table class="w-full text-sm text-left">
                        <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                            <tr>
                                <th class="px-4 py-3">Date</th>
                                <th class="px-4 py-3">Type</th>
                                <th class="px-4 py-3">Description</th>
                                <th class="px-4 py-3 text-right">Amount</th>
                                <th class="px-4 py-3 text-right">Balance</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-white/5">
                            ${cur.lines.length === 0 ? html`
                                <tr>
                                    <td colspan="5" class="px-4 py-8 text-center text-zinc-500 italic">
                                        No transactions in this period
                                    </td>
                                </tr>
                            ` : cur.lines.map((line) => html`
                                <tr class="hover:bg-white/5 transition-colors">
                                    <td class="px-4 py-3 font-mono text-zinc-300">${line.date}</td>
                                    <td class="px-4 py-3">
                                        <span class="px-2 py-0.5 rounded text-xs font-bold uppercase ${
                                            line.type === 'INVOICE' ? 'text-blue-400 bg-blue-500/10' :
                                            line.type === 'PAYMENT' || line.type === 'DISCOUNT' ? 'text-emerald-400 bg-emerald-500/10' :
                                            line.type === 'CREDIT_MEMO' || line.type === 'REFUND' ? 'text-amber-400 bg-amber-500/10' :
                                                'text-zinc-400 bg-zinc-500/10'
                                        }">
                                            ${line.type.replace('_', ' ')}
                                        </span>
                                    </td>
                                    <td class="px-4 py-3 text-zinc-300">${line.description}</td>
                                    <td class="px-4 py-3 text-right font-mono ${line.amount_cents > 0 ? 'text-rose-400' : 'text-emerald-400'}">
                                        ${line.amount_cents > 0 ? formatCents(line.amount_cents) : formatCents(-line.amount_cents)}
                                    </td>
                                    <td class="px-4 py-3 text-right font-mono text-white font-bold">
                                        ${formatCents(line.balance_after_cents)}
                                    </td>
                                </tr>
                            `)}
                        </tbody>
                    </table>

                    ${cur.open_documents.length > 0 ? html`
                        <div class="mt-6">
                            <h3 class="text-sm font-semibold text-zinc-400 uppercase tracking-wider mb-3">Open documents</h3>
                            <table class="w-full text-sm text-left">
                                <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                                    <tr>
                                        <th class="px-4 py-2">Document</th>
                                        <th class="px-4 py-2">Date</th>
                                        <th class="px-4 py-2">Due</th>
                                        <th class="px-4 py-2 text-right">Total</th>
                                        <th class="px-4 py-2 text-right">Open</th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${cur.open_documents.map(doc => html`
                                        <tr>
                                            <td class="px-4 py-2 font-mono ${doc.kind === 'invoice' ? 'text-white' : 'text-teal-300'}">${doc.number}</td>
                                            <td class="px-4 py-2 text-zinc-400">${doc.date}</td>
                                            <td class="px-4 py-2 text-zinc-400">${doc.due_date || 'Not set'}</td>
                                            <td class="px-4 py-2 text-right font-mono text-zinc-300">${formatCents(doc.total_cents)}</td>
                                            <td class="px-4 py-2 text-right font-mono text-amber-400">${formatCents(doc.open_cents)}</td>
                                        </tr>
                                    `)}
                                </tbody>
                            </table>
                        </div>
                    ` : nothing}

                    <div class="flex justify-end mt-4 pt-4 border-t border-white/10">
                        <div class="text-right">
                            <p class="text-xs text-zinc-500">Closing Balance</p>
                            <p class="text-2xl font-mono font-bold text-white">${formatCents(cur.closing_balance_cents)}</p>
                        </div>
                    </div>
                </div>
            </div>
        `;
    }
}
