// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { WalkTruncatedError } from '../../lib/cursorWalk';
import { formatCents, formatDay } from '../../lib/utils.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { ApiError, apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import { keyed } from 'lit/directives/keyed.js';
import { AccountService } from '../../services/AccountService.ts';
import { SalesTeamService } from '../../services/SalesTeamService.ts';
import type { Customer, CustomerRequest, CustomerUpdateRequest, PaymentTermsRecord } from '../../types/customer.ts';
import type { SalesPerson } from '../../types/salesteam.ts';
import type { AccountSummary, CustomerTransaction } from '../../types/account.ts';
import type { Payment } from '../../types/payment.ts';
import { paymentService } from '../../services/paymentService.ts';
import { ArrowLeft, CreditCard, Receipt, FileText, Activity, AlertCircle, Users, MessageSquare, User, Mail, Phone, ChevronDown, MapPin } from 'lucide';

// Side-effect imports: register child custom elements
import './ContactList.ts';
import './ShipToList.ts';
import './CustomerForm.ts';
import './ActivityFeed.ts';

@customElement('gable-account-detail')
export class GableAccountDetail extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'route-id' }) routeId = '';

    @state() private customer: Customer | null = null;
    @state() private summary: AccountSummary | null = null;
    @state() private transactions: CustomerTransaction[] = [];
    @state() private txnCursor: string | null = null;
    @state() private txnLoadingMore = false;
    @state() private unappliedPayments: Payment[] = [];
    @state() private openInvoices: { id: string; number: string; open_cents: number }[] = [];
    @state() private paymentsLoading = false;
    @state() private paymentsLoaded = false;
    @state() private applying: Payment | null = null;
    @state() private voiding: Payment | null = null;
    @state() private applyInvoiceId = '';
    @state() private applyAmountDollars = 0;
    @state() private voidReason = '';
    @state() private paymentBusy = false;
    @state() private loading = true;
    @state() private activeTab: 'ledger' | 'invoices' | 'payments' | 'shipto' | 'contacts' | 'crm' = 'ledger';
    @state() private salesperson: SalesPerson | null = null;
    @state() private salesTeam: SalesPerson[] = [];
    @state() private showSpDropdown = false;
    @state() private assigningRep = false;
    @state() private editing = false;
    @state() private saving = false;
    @state() private terms: PaymentTermsRecord[] = [];
    @state() private editErrors: Record<string, string> = {};
    @state() private editMessage = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.routeId) {
            this.loadData(this.routeId);
        }
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('routeId') && changed.get('routeId') !== undefined && this.routeId) {
            this.loading = true;
            this.loadData(this.routeId);
        }
    }

    private async loadData(customerId: string) {
        try {
            const [cust, summ, txnPage] = await Promise.all([
                CustomerService.getCustomer(customerId),
                AccountService.getAccountSummary(customerId),
                AccountService.getTransactions(customerId)
            ]);
            this.customer = cust;
            this.summary = summ;
            // The ledger's list envelope; the cursor walks it ("Load more"
            // follows when the account outgrows one page).
            this.transactions = txnPage.items;
            this.txnCursor = txnPage.next_cursor;
            if (cust.salesperson_id) {
                try {
                    const sp = await SalesTeamService.getSalesPerson(cust.salesperson_id);
                    this.salesperson = sp;
                } catch { ToastService.show('Failed to load salesperson details', 'error'); }
            }
        } catch (error) {
            console.error('Failed to load account data:', error);
            ToastService.show('Failed to load account data', 'error');
        } finally {
            this.loading = false;
        }
    }

    /** The next page of the ledger, appended below the rows already shown. */
    private async loadMoreTransactions() {
        if (!this.customer || !this.txnCursor || this.txnLoadingMore) return;
        this.txnLoadingMore = true;
        try {
            const page = await AccountService.getTransactions(this.customer.id, this.txnCursor);
            this.transactions = [...this.transactions, ...page.items];
            this.txnCursor = page.next_cursor;
        } catch (error) {
            console.error('Failed to load more transactions:', error);
            ToastService.show('Failed to load more transactions', 'error');
        } finally {
            this.txnLoadingMore = false;
        }
    }

    private async loadPayments(customerId: string) {
        this.paymentsLoading = true;
        try {
            const [unapplied, open] = await Promise.all([
                AccountService.getUnappliedPayments(customerId),
                AccountService.getOpenInvoices(customerId)
            ]);
            this.unappliedPayments = unapplied;
            this.openInvoices = open;
        } catch (error) {
            console.error('Failed to load payments:', error);
            ToastService.show(error instanceof WalkTruncatedError ? error.message : 'Failed to load payments', 'error');
        } finally {
            this.paymentsLoading = false;
        }
    }

    /** A tab switch; the payments tab reads the unapplied cash and the open
     * invoices once it is first shown, not on every account load. */
    private async handleTab(tab: 'ledger' | 'invoices' | 'payments' | 'shipto' | 'contacts' | 'crm') {
        this.activeTab = tab;
        if (tab === 'payments' && !this.paymentsLoaded && this.customer) {
            this.paymentsLoaded = true;
            await this.loadPayments(this.customer.id);
        }
    }

    private async handleChangeSalesperson(spId: string | null) {
        if (!this.customer) return;
        this.assigningRep = true;
        try {
            const updated = await CustomerService.updateSalesperson(this.customer.id, spId, this.customer.revision);
            this.customer = updated;
            if (spId) {
                const sp = await SalesTeamService.getSalesPerson(spId);
                this.salesperson = sp;
            } else {
                this.salesperson = null;
            }
        } catch (error) {
            console.error('Failed to assign salesperson:', error);
            if (!(await this.handleStale(error))) {
                ToastService.show(`Failed to assign salesperson: ${apiErrorMessage(error)}`, 'error');
            }
        } finally {
            this.assigningRep = false;
            this.showSpDropdown = false;
        }
    }

    /** A 409 stale_revision means another session moved the customer: show the server's message and reload it. */
    private async handleStale(error: unknown): Promise<boolean> {
        if (!(error instanceof ApiError && error.isStaleRevision) || !this.customer) return false;
        ToastService.show(`${error.message} The account was reloaded.`, 'error');
        this.editing = false;
        try {
            const fresh = await CustomerService.getCustomer(this.customer.id);
            this.customer = fresh;
            if (fresh.salesperson_id) {
                this.salesperson = await SalesTeamService.getSalesPerson(fresh.salesperson_id);
            } else {
                this.salesperson = null;
            }
        } catch (err) {
            ToastService.show(`Failed to reload the account: ${apiErrorMessage(err)}`, 'error');
        }
        return true;
    }

    private async openEdit() {
        this.editErrors = {};
        this.editMessage = '';
        this.editing = true;
        if (this.terms.length === 0) {
            try {
                this.terms = (await CustomerService.listPaymentTerms({ limit: 200 })).items;
            } catch (error) {
                this.editMessage = apiErrorMessage(error, 'Failed to load payment terms');
            }
        }
    }

    /** Saves the whole header on the revision this page loaded. */
    private async saveHeader(request: CustomerRequest) {
        if (!this.customer) return;
        this.saving = true;
        this.editErrors = {};
        this.editMessage = '';
        try {
            // A PUT must name the controls it keeps, so a form that left one out sends the loaded value.
            const update: CustomerUpdateRequest = {
                ...request,
                payment_terms_id: request.payment_terms_id ?? this.customer.payment_terms_id,
                po_required: request.po_required ?? this.customer.po_required,
            };
            this.customer = await CustomerService.updateCustomer(this.customer.id, update, this.customer.revision);
            this.editing = false;
            ToastService.show('Customer saved', 'success');
            // The balance summary's limit may have moved with the header.
            this.summary = await AccountService.getAccountSummary(this.customer.id);
        } catch (error) {
            if (!(await this.handleStale(error))) {
                this.editErrors = fieldErrorMap(error);
                this.editMessage = apiErrorMessage(error, 'Failed to save customer');
            }
        } finally {
            this.saving = false;
        }
    }

    private async openSpDropdown() {
        if (this.salesTeam.length === 0) {
            try {
                const team = await SalesTeamService.listSalesTeam();
                this.salesTeam = team;
            } catch { ToastService.show('Failed to load sales team', 'error'); }
        }
        this.showSpDropdown = !this.showSpDropdown;
    }

    private renderTab(tabId: 'ledger' | 'invoices' | 'payments' | 'shipto' | 'contacts' | 'crm', label: string, iconData: typeof Activity) {
        const active = this.activeTab === tabId;
        return html`
            <button
                @click=${() => { this.handleTab(tabId); }}
                class="flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 transition-colors ${
                    active
                        ? 'text-gable-green border-gable-green bg-gable-green/5'
                        : 'text-zinc-400 border-transparent hover:text-white hover:border-white/20'
                }"
            >
                ${icon(iconData, 16)}
                ${label}
            </button>
        `;
    }

    /** Opens the apply dialog on a payment, defaulting to its oldest open invoice. */
    private openApply(pay: Payment) {
        this.applying = pay;
        this.applyInvoiceId = this.openInvoices[0]?.id ?? '';
        this.applyAmountDollars = pay.unapplied_cents / 100;
    }

    /** Applies unapplied cash to one invoice (the revision is the precondition). */
    private async submitApply(e: Event) {
        e.preventDefault();
        if (!this.applying || !this.customer) return;
        const amountCents = Math.round(this.applyAmountDollars * 100);
        if (amountCents <= 0 || !this.applyInvoiceId) return;
        this.paymentBusy = true;
        try {
            await paymentService.apply(this.applying.id, [{ invoice_id: this.applyInvoiceId, amount_cents: amountCents }], this.applying.revision);
            ToastService.show(`Applied ${formatCents(amountCents)} of ${this.applying.number}`, 'success');
            this.applying = null;
            await Promise.all([this.loadPayments(this.customer.id), this.loadData(this.customer.id)]);
        } catch (error) {
            ToastService.show(error instanceof Error ? error.message : 'Failed to apply the payment', 'error');
        } finally {
            this.paymentBusy = false;
        }
    }

    /** Voids a posted payment: every application reversed, the invoices reopened. */
    private async submitVoid(e: Event) {
        e.preventDefault();
        if (!this.voiding || !this.customer || !this.voidReason.trim()) return;
        this.paymentBusy = true;
        try {
            await paymentService.void(this.voiding.id, this.voidReason.trim(), this.voiding.revision);
            ToastService.show(`Payment ${this.voiding.number} voided`, 'success');
            this.voiding = null;
            await Promise.all([this.loadPayments(this.customer.id), this.loadData(this.customer.id)]);
        } catch (error) {
            ToastService.show(error instanceof Error ? error.message : 'Failed to void the payment', 'error');
        } finally {
            this.paymentBusy = false;
        }
    }

    private renderApplyDialog() {
        if (!this.applying) return nothing;
        const invoice = this.openInvoices.find(i => i.id === this.applyInvoiceId);
        return html`
            <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm" role="dialog" aria-modal="true" aria-labelledby="apply-dialog-title">
                <div class="w-full max-w-md bg-zinc-900 border border-zinc-700 rounded-lg shadow-2xl p-6">
                    <h2 id="apply-dialog-title" class="text-xl font-bold text-zinc-100 mb-1">Apply unapplied cash</h2>
                    <p class="text-zinc-400 text-sm mb-6">${this.applying.number} holds ${formatCents(this.applying.unapplied_cents)} unapplied</p>
                    <form @submit=${this.submitApply} class="space-y-4">
                        <div>
                            <label class="block text-sm font-medium text-zinc-400 mb-1">Invoice</label>
                            <select
                                .value=${this.applyInvoiceId}
                                @change=${(e: Event) => this.applyInvoiceId = (e.target as HTMLSelectElement).value}
                                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100"
                            >
                                ${this.openInvoices.map(i => html`<option value=${i.id}>${i.number} (${formatCents(i.open_cents)} open)</option>`)}
                            </select>
                            ${invoice ? html`<p class="mt-1 text-xs text-zinc-500">${invoice.number} has ${formatCents(invoice.open_cents)} open</p>` : nothing}
                        </div>
                        <div>
                            <label class="block text-sm font-medium text-zinc-400 mb-1">Amount to apply</label>
                            <input type="number" required min="0.01" step="0.01"
                                .value=${String(this.applyAmountDollars)}
                                @input=${(e: InputEvent) => this.applyAmountDollars = parseFloat((e.target as HTMLInputElement).value)}
                                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100 font-mono"
                            />
                        </div>
                        <div class="mt-8 flex justify-end gap-3">
                            <button type="button" @click=${() => { this.applying = null; }} class="px-4 py-2 text-sm text-zinc-300 hover:text-white">Cancel</button>
                            <button type="submit" ?disabled=${this.paymentBusy} class="px-4 py-2 bg-green-600 hover:bg-green-500 text-white rounded text-sm font-medium disabled:opacity-50" data-testid="confirm-apply">
                                ${this.paymentBusy ? 'Applying...' : 'Apply'}
                            </button>
                        </div>
                    </form>
                </div>
            </div>
        `;
    }

    private renderVoidDialog() {
        if (!this.voiding) return nothing;
        return html`
            <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm" role="dialog" aria-modal="true" aria-labelledby="void-dialog-title">
                <div class="w-full max-w-md bg-zinc-900 border border-zinc-700 rounded-lg shadow-2xl p-6">
                    <h2 id="void-dialog-title" class="text-xl font-bold text-zinc-100 mb-1">Void payment</h2>
                    <p class="text-zinc-400 text-sm mb-6">Voiding ${this.voiding.number} reverses every application of it and reopens its invoices${this.voiding.method === 'card' ? '; a card payment is refunded through the gateway instead' : ''}.</p>
                    <form @submit=${this.submitVoid} class="space-y-4">
                        <div>
                            <label class="block text-sm font-medium text-zinc-400 mb-1">Reason</label>
                            <input type="text" required maxlength="500"
                                .value=${this.voidReason}
                                @input=${(e: InputEvent) => this.voidReason = (e.target as HTMLInputElement).value}
                                class="w-full bg-zinc-950 border border-zinc-700 rounded px-3 py-2 text-zinc-100"
                                placeholder="Why is this payment being voided?"
                            />
                        </div>
                        <div class="mt-8 flex justify-end gap-3">
                            <button type="button" @click=${() => { this.voiding = null; }} class="px-4 py-2 text-sm text-zinc-300 hover:text-white">Cancel</button>
                            <button type="submit" ?disabled=${this.paymentBusy || !this.voidReason.trim()} class="px-4 py-2 bg-red-600 hover:bg-red-500 text-white rounded text-sm font-medium disabled:opacity-50" data-testid="confirm-void">
                                ${this.paymentBusy ? 'Voiding...' : 'Void payment'}
                            </button>
                        </div>
                    </form>
                </div>
            </div>
        `;
    }

    private renderTxnTypeBadge(type: string) {
        let cls = 'bg-blue-500/10 text-blue-400';
        if (type === 'INVOICE') cls = 'bg-orange-500/10 text-orange-400';
        if (type === 'PAYMENT' || type === 'DISCOUNT') cls = 'bg-emerald-500/10 text-emerald-400';
        if (type === 'CREDIT_MEMO' || type === 'REFUND') cls = 'bg-teal-500/10 text-teal-300';
        if (type === 'WRITE_OFF') cls = 'bg-red-500/10 text-red-400';
        if (type === 'REVERSAL') cls = 'bg-zinc-500/10 text-zinc-400';
        return html`
            <span class="px-2 py-0.5 rounded text-xs font-bold uppercase tracking-wider ${cls}">
                ${type.replace('_', ' ')}
            </span>
        `;
    }

    render() {
        if (this.loading) {
            return html`<div class="text-white p-8">Loading account...</div>`;
        }

        if (!this.customer || !this.summary) {
            return html`<div class="p-8 text-center text-zinc-400">Account not found</div>`;
        }

        const customer = this.customer;
        const summary = this.summary;
        // A null limit is no limit; the summary reads it as 0, so the cards say "No limit" instead.
        const noLimit = customer.credit_limit_cents === null;
        const limitCents = summary.credit_limit_cents ?? 0;
        const availablePercentage = limitCents > 0
            ? ((summary.available_credit_cents ?? 0) / limitCents) * 100
            : 0;

        return html`
            <div class="space-y-8 max-w-6xl mx-auto">
                <!-- Header -->
                <div>
                    <a href="/accounts" class="inline-flex items-center text-sm text-zinc-400 hover:text-white mb-4 transition-colors">
                        ${icon(ArrowLeft, 16, 'mr-1')} Back to Accounts
                    </a>
                    <div class="flex items-start justify-between">
                        <div>
                            <h1 class="text-3xl font-bold text-white">${customer.name}</h1>
                            <div class="flex items-center gap-3 mt-2 text-zinc-400 text-sm">
                                <span class="font-mono bg-white/5 px-2 py-0.5 rounded border border-white/5">#${customer.account_number}</span>
                                <span>${customer.email}</span>
                                <span>&bull;</span>
                                <span>${customer.phone}</span>
                            </div>
                            <dl class="flex flex-wrap items-center gap-x-6 gap-y-1 mt-3 text-sm" data-account-facts>
                                <div class="flex gap-2"><dt class="text-zinc-500">Terms</dt><dd class="text-white" data-fact="terms">${customer.payment_terms.code} - ${customer.payment_terms.name}</dd></div>
                                <div class="flex gap-2"><dt class="text-zinc-500">PO required</dt><dd class="text-white" data-fact="po">${customer.po_required ? 'Yes' : 'No'}</dd></div>
                                <div class="flex gap-2"><dt class="text-zinc-500">Currency</dt><dd class="font-mono text-white" data-fact="currency">${customer.effective_currency}${customer.currency === null ? ' (dealer default)' : ''}</dd></div>
                                <div class="flex gap-2"><dt class="text-zinc-500">Tier</dt><dd class="text-white capitalize" data-fact="tier">${customer.tier}</dd></div>
                                <div class="flex gap-2"><dt class="text-zinc-500">Revision</dt><dd class="font-mono text-white" data-fact="revision">${customer.revision}</dd></div>
                            </dl>
                        </div>
                        <!-- "New Transaction" was never given a target, so it stays disabled rather than looking live. -->
                        <div class="flex gap-2">
                            <button
                                @click=${() => void this.openEdit()}
                                ?disabled=${this.editing}
                                class="border border-white/10 text-zinc-300 hover:text-white px-3 py-1.5 rounded text-sm font-medium transition-colors disabled:opacity-50"
                            >Edit Profile</button>
                            <button
                                disabled
                                title="Not implemented: start a quote from Quotes > Quote Builder instead."
                                class="border border-white/10 text-zinc-500 px-3 py-1.5 rounded text-sm font-medium opacity-50 cursor-not-allowed"
                            >New Transaction</button>
                        </div>
                    </div>
                </div>

                ${this.editing ? keyed(customer.revision, html`
                    <gable-customer-form
                        .customer=${customer}
                        .terms=${this.terms}
                        .fieldErrors=${this.editErrors}
                        .formError=${this.editMessage}
                        ?submitting=${this.saving}
                        @customer-submit=${(e: CustomEvent<CustomerRequest>) => void this.saveHeader(e.detail)}
                        @customer-cancel=${() => { this.editing = false; }}
                    ></gable-customer-form>
                `) : nothing}

                <!-- Salesperson Card -->
                <div class="p-5 bg-slate-steel border border-white/10 rounded-2xl relative">
                    <div class="flex items-center justify-between">
                        <div class="flex items-center gap-4">
                            <div class="w-10 h-10 rounded-full bg-blue-500/20 flex items-center justify-center">
                                ${icon(User, 20, 'text-blue-400')}
                            </div>
                            ${this.salesperson ? html`
                                <div>
                                    <p class="text-white font-medium">${this.salesperson.name}</p>
                                    <div class="flex items-center gap-3 text-sm text-zinc-400">
                                        <span class="px-2 py-0.5 rounded text-xs font-medium bg-blue-500/10 text-blue-400">${this.salesperson.role}</span>
                                        <span class="flex items-center gap-1">${icon(Mail, 12)} ${this.salesperson.email}</span>
                                        <span class="flex items-center gap-1">${icon(Phone, 12)} ${this.salesperson.phone}</span>
                                    </div>
                                </div>
                            ` : customer.salesperson_name ? html`
                                <div>
                                    <p class="text-white font-medium">${customer.salesperson_name}</p>
                                    <div class="text-xs text-zinc-500">Assigned Rep</div>
                                </div>
                            ` : html`
                                <div>
                                    <p class="text-zinc-500 text-sm">No salesperson assigned</p>
                                </div>
                            `}
                        </div>
                        <div class="relative">
                            <button
                                @click=${() => this.openSpDropdown()}
                                ?disabled=${this.assigningRep}
                                class="border border-white/10 text-zinc-300 hover:text-white px-3 py-1.5 rounded text-sm font-medium transition-colors flex items-center gap-1 disabled:opacity-50"
                            >
                                ${this.assigningRep ? 'Saving...' : this.salesperson ? 'Change' : 'Assign Salesperson'}
                                ${icon(ChevronDown, 14, 'ml-1')}
                            </button>
                            ${this.showSpDropdown ? html`
                                <div class="fixed inset-0 z-40" @click=${() => { this.showSpDropdown = false; }}></div>
                                <div class="absolute right-0 top-full mt-2 w-64 bg-slate-800 border border-white/10 rounded-lg shadow-xl z-50">
                                    ${this.salesTeam.map(sp => html`
                                        <button
                                            @click=${() => this.handleChangeSalesperson(sp.id)}
                                            class="w-full px-4 py-3 text-left text-sm hover:bg-white/5 transition-colors flex items-center justify-between first:rounded-t-lg ${
                                                sp.id === customer?.salesperson_id ? 'bg-gable-green/10 text-gable-green' : 'text-white'
                                            }"
                                        >
                                            <div>
                                                <div class="font-medium">${sp.name}</div>
                                                <div class="text-xs text-zinc-400">${sp.role}</div>
                                            </div>
                                            ${sp.id === customer?.salesperson_id ? html`
                                                <span class="text-xs text-gable-green">Current</span>
                                            ` : nothing}
                                        </button>
                                    `)}
                                    ${this.salesperson ? html`
                                        <button
                                            @click=${() => this.handleChangeSalesperson(null)}
                                            class="w-full px-4 py-3 text-left text-sm text-red-400 hover:bg-white/5 transition-colors border-t border-white/10 rounded-b-lg"
                                        >
                                            Unassign Salesperson
                                        </button>
                                    ` : nothing}
                                </div>
                            ` : nothing}
                        </div>
                    </div>
                </div>

                <!-- Financial Overview Cards -->
                <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <div class="p-5 border-l-4 border-l-orange-500 bg-gradient-to-br from-slate-steel to-transparent rounded-lg border border-white/10">
                        <div class="flex items-center gap-3 mb-2 text-zinc-400 text-sm font-medium uppercase tracking-wide">
                            ${icon(Receipt, 16, 'text-orange-400')}
                            Balance Due
                        </div>
                        <div class="text-3xl font-mono font-bold text-white">
                            ${formatCents(summary.balance_cents)}
                        </div>
                        <div class="mt-2 text-xs text-zinc-500">Current outstanding balance</div>
                    </div>

                    <div class="p-5 border-l-4 border-l-emerald-500 bg-gradient-to-br from-slate-steel to-transparent rounded-lg border border-white/10">
                        <div class="flex items-center gap-3 mb-2 text-zinc-400 text-sm font-medium uppercase tracking-wide">
                            ${icon(CreditCard, 16, 'text-emerald-400')}
                            Available Credit
                        </div>
                        <div class="text-3xl font-mono font-bold ${!noLimit && availablePercentage < 20 ? 'text-red-400' : 'text-white'}">
                            ${noLimit ? 'No limit' : formatCents(summary.available_credit_cents ?? 0)}
                        </div>
                        <div class="mt-2 w-full bg-white/10 h-1.5 rounded-full overflow-hidden">
                            <div
                                class="h-full rounded-full transition-all duration-500 ${!noLimit && availablePercentage < 20 ? 'bg-red-500' : 'bg-emerald-500'}"
                                style="width: ${noLimit ? 100 : Math.min(availablePercentage, 100)}%"
                            ></div>
                        </div>
                    </div>

                    <div class="p-5 border-l-4 border-l-blue-500 bg-gradient-to-br from-slate-steel to-transparent rounded-lg border border-white/10">
                        <div class="flex items-center gap-3 mb-2 text-zinc-400 text-sm font-medium uppercase tracking-wide">
                            ${icon(Activity, 16, 'text-blue-400')}
                            Credit Limit
                        </div>
                        <div class="text-3xl font-mono font-bold text-white">
                            ${noLimit ? 'No limit' : formatCents(customer.credit_limit_cents)}
                        </div>
                        <div class="mt-2 text-xs text-zinc-500">${noLimit ? 'No credit ceiling on this account' : customer.credit_limit_cents === 0 ? 'No credit: cash only' : 'Total approved credit line'}</div>
                    </div>
                </div>

                <!-- Tabs & Content -->
                <div class="space-y-4">
                    <div class="flex items-center gap-1 border-b border-white/10 pb-1">
                        ${this.renderTab('ledger', 'Activity Ledger', Activity)}
                        ${this.renderTab('invoices', 'Invoices', FileText)}
                        ${this.renderTab('payments', 'Payments', CreditCard)}
                        ${this.renderTab('shipto', 'Ship-to Addresses', MapPin)}
                        ${this.renderTab('contacts', 'Contacts', Users)}
                        ${this.renderTab('crm', 'CRM Activity', MessageSquare)}
                    </div>

                    <div class="min-h-[400px]">
                        ${this.activeTab === 'ledger' ? html`
                            <div class="border border-white/5 rounded-lg overflow-hidden bg-slate-steel/20">
                                <table class="w-full text-sm">
                                    <thead class="bg-white/5 text-zinc-400 font-medium border-b border-white/5">
                                        <tr>
                                            <th class="px-4 py-3 text-left">Date</th>
                                            <th class="px-4 py-3 text-left">Type</th>
                                            <th class="px-4 py-3 text-left">Description</th>
                                            <th class="px-4 py-3 text-right">Amount</th>
                                            <th class="px-4 py-3 text-right">Running Balance</th>
                                        </tr>
                                    </thead>
                                    <tbody class="divide-y divide-white/5">
                                        ${this.transactions.length === 0 ? html`
                                            <tr>
                                                <td colspan="5" class="px-4 py-8 text-center text-zinc-500">No transactions found</td>
                                            </tr>
                                        ` : this.transactions.map(txn => html`
                                            <tr class="group hover:bg-white/5 transition-colors">
                                                <td class="px-4 py-3 font-mono text-zinc-400">
                                                    ${new Date(txn.created_at).toLocaleDateString()}
                                                </td>
                                                <td class="px-4 py-3">
                                                    ${this.renderTxnTypeBadge(txn.type)}
                                                </td>
                                                <td class="px-4 py-3 text-white">${txn.description}</td>
                                                <td class="px-4 py-3 text-right font-mono font-medium ${txn.amount_cents > 0 ? 'text-white' : 'text-emerald-400'}">
                                                    ${txn.amount_cents > 0 ? '+' : ''}${formatCents(txn.amount_cents)}
                                                </td>
                                                <td class="px-4 py-3 text-right font-mono text-zinc-300">
                                                    ${formatCents(txn.balance_after_cents)}
                                                </td>
                                            </tr>
                                        `)}
                                    </tbody>
                                </table>
                            </div>
                            ${this.txnCursor ? html`
                                <div class="mt-3 flex justify-center">
                                    <button type="button" data-testid="ledger-load-more" ?disabled=${this.txnLoadingMore}
                                        @click=${() => this.loadMoreTransactions()}
                                        class="px-4 py-2 text-sm rounded border border-zinc-700 text-zinc-300 hover:bg-white/5 disabled:opacity-50">
                                        ${this.txnLoadingMore ? 'Loading...' : 'Load more'}
                                    </button>
                                </div>` : nothing}
                        ` : nothing}

                        ${this.activeTab === 'payments' ? html`
                            <div class="space-y-4" data-testid="payments-tab">
                                <div class="flex items-center justify-between">
                                    <div>
                                        <h3 class="text-white font-semibold">Unapplied cash</h3>
                                        <p class="text-zinc-500 text-sm">Cash held in deposits until it is applied to an invoice, refunded or voided</p>
                                    </div>
                                </div>
                                ${this.paymentsLoading ? html`
                                    <div class="p-8 text-center text-zinc-500">Loading payments...</div>
                                ` : this.unappliedPayments.length === 0 ? html`
                                    <div class="p-8 text-center text-zinc-500 border border-white/5 rounded-lg">No unapplied cash on this account</div>
                                ` : html`
                                    <div class="border border-white/5 rounded-lg overflow-hidden bg-slate-steel/20">
                                        <table class="w-full text-sm" aria-label="Unapplied payments">
                                            <thead class="bg-white/5 text-zinc-400 font-medium border-b border-white/5">
                                                <tr>
                                                    <th class="px-4 py-3 text-left">Payment</th>
                                                    <th class="px-4 py-3 text-left">Received</th>
                                                    <th class="px-4 py-3 text-left">Method</th>
                                                    <th class="px-4 py-3 text-left">Reference</th>
                                                    <th class="px-4 py-3 text-right">Amount</th>
                                                    <th class="px-4 py-3 text-right">Unapplied</th>
                                                    <th class="px-4 py-3 text-right">Actions</th>
                                                </tr>
                                            </thead>
                                            <tbody class="divide-y divide-white/5">
                                                ${this.unappliedPayments.map(pay => html`
                                                    <tr class="hover:bg-white/5 transition-colors" data-testid="unapplied-payment">
                                                        <td class="px-4 py-3 font-mono text-white">${pay.number}</td>
                                                        <td class="px-4 py-3 text-zinc-400">${formatDay(pay.received_on)}</td>
                                                        <td class="px-4 py-3 text-zinc-300">${pay.method}</td>
                                                        <td class="px-4 py-3 text-zinc-400 font-mono text-xs">${pay.reference || '-'}</td>
                                                        <td class="px-4 py-3 text-right font-mono text-zinc-300">${formatCents(pay.amount_cents)}</td>
                                                        <td class="px-4 py-3 text-right font-mono text-amber-400">${formatCents(pay.unapplied_cents)}</td>
                                                        <td class="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                                                            <button
                                                                class="px-3 py-1.5 text-xs rounded bg-green-600 hover:bg-green-500 text-white font-medium"
                                                                data-testid="apply-payment"
                                                                @click=${() => this.openApply(pay)}
                            ?disabled=${this.openInvoices.length === 0}
                                                        >Apply</button>
                                                            <button
                                                                class="px-3 py-1.5 text-xs rounded bg-zinc-700 hover:bg-zinc-600 text-zinc-200 font-medium"
                                                                data-testid="void-payment"
                                                                @click=${() => { this.voiding = pay; this.voidReason = ''; }}
                                                            >Void</button>
                                                        </td>
                                                    </tr>
                                                `)}
                                            </tbody>
                                        </table>
                                    </div>
                                    ${this.openInvoices.length === 0 ? html`
                                        <p class="text-zinc-500 text-sm">No open invoices to apply cash to.</p>
                                    ` : nothing}
                                `}
                            </div>
                        ` : nothing}

                        ${this.activeTab === 'shipto' ? html`
                            <div class="mt-4">
                                <gable-ship-to-list customerId=${customer.id}></gable-ship-to-list>
                            </div>
                        ` : nothing}

                        ${this.activeTab === 'contacts' ? html`
                            <div class="mt-4">
                                <gable-contact-list customerId=${customer.id}></gable-contact-list>
                            </div>
                        ` : nothing}

                        ${this.activeTab === 'crm' ? html`
                            <div class="mt-4">
                                <gable-activity-feed customerId=${customer.id}></gable-activity-feed>
                            </div>
                        ` : nothing}

                        ${(this.activeTab !== 'ledger' && this.activeTab !== 'payments' && this.activeTab !== 'shipto' && this.activeTab !== 'contacts' && this.activeTab !== 'crm') ? html`
                            <div class="flex flex-col items-center justify-center h-64 border border-dashed border-white/10 rounded-lg text-zinc-500">
                                ${icon(AlertCircle, 32, 'mb-2 opacity-50')}
                                <p>This view is under construction.</p>
                            </div>
                        ` : nothing}
                    </div>
                </div>
            </div>
            ${this.renderApplyDialog()}
            ${this.renderVoidDialog()}
        `;
    }
}
