// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state, property } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { router } from '../../lib/router.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { ArrowRight, ShoppingCart, BarChart3, ListChecks, Send, Check, X, List, FilePlus, Pencil, Truck, Package, Clock } from 'lucide';
import { QuoteService, QuoteApiError, quoteErrorMessage } from '../../services/QuoteService.ts';
import type { QuoteFollowUpFilter } from '../../services/QuoteService.ts';
import type { QuoteSummary, QuoteStatus } from '../../types/quote.ts';
import { formatCents } from '../../lib/utils.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { formatDate, formatDay } from '../../lib/dates.ts';
import { followUpDay, followUpOverdue } from '../../lib/followUp.ts';

@customElement('gable-quote-view-tabs')
export class GableQuoteViewTabs extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) active: 'list' | 'new' = 'list';

    render() {
        return html`
            <div class="flex gap-1 mb-6 border-b border-white/10">
                <a
                    href="/quotes"
                    class="flex items-center gap-2 px-5 py-3 text-sm font-medium transition-colors relative ${
                        this.active === 'list' ? 'text-gable-green' : 'text-zinc-400 hover:text-white'
                    }"
                >
                    ${icon(List, 16)} All Quotes
                    ${this.active === 'list' ? html`<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-gable-green"></span>` : nothing}
                </a>
                <a
                    href="/quotes/new"
                    class="flex items-center gap-2 px-5 py-3 text-sm font-medium transition-colors relative ${
                        this.active === 'new' ? 'text-gable-green' : 'text-zinc-400 hover:text-white'
                    }"
                >
                    ${icon(FilePlus, 16)} New Quote
                    ${this.active === 'new' ? html`<span class="absolute bottom-0 left-0 right-0 h-0.5 bg-gable-green"></span>` : nothing}
                </a>
            </div>
        `;
    }
}

@customElement('gable-quote-list')
export class GableQuoteList extends LitElement {
    createRenderRoot() { return this; }

    @state() private quotes: QuoteSummary[] = [];
    @state() private nextCursor: string | null = null;
    @state() private total: number | null = null;
    @state() private loadingMore = false;
    @state() private loading = true;
    @state() private error: string | null = null;
    @state() private converting: string | null = null;
    @state() private updatingState: string | null = null;

    private stateColors: Record<string, string> = {
        draft: 'bg-voice-fill/20 text-voice-text border-voice-fill/30',
        sent: 'bg-blue-500/20 text-blue-400 border-blue-500/30',
        accepted: 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30',
        rejected: 'bg-red-500/20 text-red-400 border-red-500/30',
        expired: 'bg-amber-500/20 text-amber-400 border-amber-500/30',
    };

    private _unsubBranch: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        this.loadQuotes();
        this._unsubBranch = onBranchChanged(() => {
            this.loading = true;
            this.loadQuotes();
        });
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        if (this._unsubBranch) {
            this._unsubBranch();
            this._unsubBranch = null;
        }
    }

    private static readonly PAGE_SIZE = 50;
    private static readonly STATUSES: QuoteStatus[] = ['draft', 'sent', 'accepted', 'rejected', 'expired'];

    /** An optional ?status=sent,draft on the page URL narrows the list on the server. */
    private get statusFilter(): QuoteStatus[] {
        const raw = new URLSearchParams(window.location.search).get('status') ?? '';
        return raw.split(',').map(v => v.trim().toLowerCase())
            .filter((v): v is QuoteStatus => (GableQuoteList.STATUSES as string[]).includes(v));
    }

    private static readonly FOLLOW_UPS: QuoteFollowUpFilter[] = ['due', 'overdue', 'mine'];

    /** The follow up queue the list is narrowed to, from ?follow_up= on the page URL. */
    private get followUpFilter(): QuoteFollowUpFilter | null {
        const raw = (new URLSearchParams(window.location.search).get('follow_up') ?? '').trim().toLowerCase();
        return (GableQuoteList.FOLLOW_UPS as string[]).includes(raw) ? (raw as QuoteFollowUpFilter) : null;
    }

    /** The queue tab the reader picks; the choice is in the URL, so a link carries it. */
    private setFollowUpFilter(kind: QuoteFollowUpFilter | null) {
        const params = new URLSearchParams(window.location.search);
        if (kind) params.set('follow_up', kind);
        else params.delete('follow_up');
        const q = params.toString();
        history.replaceState(null, '', q ? `${window.location.pathname}?${q}` : window.location.pathname);
        this.loading = true;
        void this.loadQuotes();
    }

    private async loadQuotes() {
        try {
            this.error = null;
            const page = await QuoteService.list({
                status: this.statusFilter,
                followUp: this.followUpFilter ?? undefined,
                limit: GableQuoteList.PAGE_SIZE,
                includeTotal: true,
            });
            this.quotes = page.items || [];
            this.nextCursor = page.next_cursor ?? null;
            this.total = page.total ?? null;
        } catch (err) {
            console.error('Failed to load quotes:', err);
            this.error = quoteErrorMessage(err, 'Failed to load quotes');
        } finally {
            this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor || this.loadingMore) return;
        this.loadingMore = true;
        try {
            const page = await QuoteService.list({
                status: this.statusFilter,
                followUp: this.followUpFilter ?? undefined,
                limit: GableQuoteList.PAGE_SIZE,
                cursor: this.nextCursor,
            });
            this.quotes = [...this.quotes, ...(page.items || [])];
            this.nextCursor = page.next_cursor ?? null;
        } catch (err) {
            ToastService.show(`Failed to load more: ${quoteErrorMessage(err)}`, 'error');
        } finally {
            this.loadingMore = false;
        }
    }

    /** A 409 stale_revision means another session moved the quote: say so and reload. */
    private async handleStale(error: unknown): Promise<boolean> {
        if (error instanceof QuoteApiError && error.isStaleRevision) {
            ToastService.show('This quote changed elsewhere. Reloaded.', 'error');
            await this.loadQuotes();
            return true;
        }
        return false;
    }

    private async handleConvert(quote: QuoteSummary) {
        this.converting = quote.id;
        try {
            const order = await QuoteService.convert(quote.id, quote.revision);
            ToastService.show('Quote converted to order successfully', 'success');
            router.navigate(`/orders/${order.id}`);
        } catch (error) {
            if (await this.handleStale(error)) return;
            ToastService.show(`Failed to convert: ${quoteErrorMessage(error, 'Unknown error')}`, 'error');
        } finally {
            this.converting = null;
        }
    }

    private async handleStateChange(quote: QuoteSummary, to: QuoteStatus) {
        this.updatingState = quote.id;
        try {
            await QuoteService.transition(quote.id, to, quote.revision);
            await this.loadQuotes();
            ToastService.show(`Quote marked as ${to}`, 'success');
        } catch (error) {
            if (await this.handleStale(error)) return;
            ToastService.show(`Failed: ${quoteErrorMessage(error, 'Unknown error')}`, 'error');
        } finally {
            this.updatingState = null;
        }
    }

    render() {
        if (this.error) {
            return html`
                <div class="space-y-6">
                    <gable-quote-view-tabs active="list"></gable-quote-view-tabs>
                    <div class="flex flex-col items-center justify-center min-h-[400px] p-8">
                        <p class="text-rose-400 text-lg font-semibold mb-2">Failed to load</p>
                        <p class="text-gray-400 text-sm mb-4">${this.error}</p>
                        <button
                            @click=${() => { this.error = null; this.loadQuotes(); }}
                            class="px-4 py-2 bg-gable-green text-deep-space rounded font-medium hover:opacity-90"
                        >
                            Retry
                        </button>
                    </div>
                </div>
            `;
        }

        return html`
            <div class="space-y-6">
                <gable-quote-view-tabs active="list"></gable-quote-view-tabs>

                <div class="flex items-center justify-between">
                    <div>
                        <h1 class="text-3xl font-bold tracking-tight text-white font-mono">Quotes</h1>
                        <p class="text-muted-foreground mt-2">Manage sales quotes and convert to orders.</p>
                    </div>
                    <div class="flex items-center gap-3">
                        <button
                            @click=${() => router.navigate('/quotes/analytics')}
                            class="border border-white/10 text-zinc-400 hover:text-white hover:border-white/20 font-medium px-4 py-2 rounded transition-colors flex items-center gap-2 text-sm"
                        >
                            ${icon(BarChart3, 16)} Analytics
                        </button>
                    </div>
                </div>

                <!-- The follow up queues: the tabs narrow the page on the server -->
                <div class="flex gap-1 border-b border-white/10" role="tablist" aria-label="Quote queues">
                    ${[
                        { id: null as QuoteFollowUpFilter | null, label: 'All quotes' },
                        { id: 'due' as QuoteFollowUpFilter | null, label: 'Follow ups due' },
                        { id: 'overdue' as QuoteFollowUpFilter | null, label: 'Overdue' },
                        { id: 'mine' as QuoteFollowUpFilter | null, label: 'Mine' },
                    ].map(tab => html`
                        <button
                            role="tab"
                            aria-selected=${this.followUpFilter === tab.id ? 'true' : 'false'}
                            @click=${() => this.setFollowUpFilter(tab.id)}
                            data-testid="follow-up-tab-${tab.id ?? 'all'}"
                            class="flex items-center gap-2 px-4 py-2.5 text-sm font-medium transition-colors border-b-2 -mb-px ${
                                this.followUpFilter === tab.id
                                    ? 'text-gable-green border-gable-green'
                                    : 'text-zinc-500 border-transparent hover:text-white hover:border-white/20'
                            }"
                        >
                            ${tab.id === 'due' ? icon(Clock, 14) : nothing}
                            ${tab.label}
                        </button>
                    `)}
                </div>

                <div class="bg-slate-steel border border-white/10 rounded-lg overflow-hidden">
                    <table class="w-full text-left text-sm" aria-label="Quotes list">
                        <thead>
                            <tr class="border-b border-white/10 bg-white/5">
                                <th class="p-4 font-medium text-muted-foreground">Quote ID</th>
                                <th class="p-4 font-medium text-muted-foreground">Date</th>
                                <th class="p-4 font-medium text-muted-foreground">Customer</th>
                                <th class="p-4 font-medium text-muted-foreground">Source</th>
                                <th class="p-4 font-medium text-muted-foreground">Fulfillment</th>
                                <th class="p-4 font-medium text-muted-foreground">Follow up</th>
                                <th class="p-4 font-medium text-muted-foreground">State</th>
                                <th class="p-4 font-medium text-muted-foreground text-right">Total</th>
                                <th class="p-4 font-medium text-muted-foreground text-right">Actions</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-white/5">
                            ${this.loading ? html`
                                <tr>
                                    <td colspan="9" class="p-8 text-center text-muted-foreground">Loading quotes...</td>
                                </tr>
                            ` : this.quotes.length === 0 ? html`
                                <tr>
                                    <td colspan="9" class="p-8 text-center text-muted-foreground">
                                        ${this.followUpFilter
                                            ? 'No follow ups are due. A follow up you set shows here on its day.'
                                            : 'No quotes found. Create your first quote to get started.'}
                                    </td>
                                </tr>
                            ` : this.quotes.map(quote => {
                                const isBusy = this.converting === quote.id || this.updatingState === quote.id;
                                const followUp = followUpDay(quote.follow_up_at);
                                const overdue = followUpOverdue(quote.follow_up_at);
                                return html`
                                    <tr class="hover:bg-white/5 transition-colors cursor-pointer" @click=${() => router.navigate(`/quotes/${quote.id}`)}>
                                        <td class="p-4 font-mono text-white/80">${quote.number}</td>
                                        <td class="p-4 text-white/80">${formatDate(quote.created_at)}</td>
                                        <td class="p-4 text-white font-medium">${quote.customer_name || quote.customer_id.slice(0, 8)}</td>
                                        <td class="p-4">
                                            ${quote.source === 'ai' ? html`
                                                <span class="inline-flex items-center gap-1 text-xs text-zinc-300">
                                                    ${icon(ListChecks, 12)} Material list
                                                </span>
                                            ` : html`
                                                <span class="text-xs text-zinc-500">Manual</span>
                                            `}
                                        </td>
                                        <td class="p-4">
                                            ${quote.delivery_type === 'delivery' ? html`
                                                <span class="inline-flex items-center gap-1 text-xs text-blue-400">
                                                    ${icon(Truck, 12)} Delivery
                                                </span>
                                            ` : html`
                                                <span class="inline-flex items-center gap-1 text-xs text-zinc-500">
                                                    ${icon(Package, 12)} Pickup
                                                </span>
                                            `}
                                        </td>
                                        <td class="p-4">
                                            ${followUp ? html`
                                                <div class="flex items-center gap-1.5">
                                                    <span class="font-mono text-xs ${overdue ? 'text-rose-400' : 'text-white/80'}" data-testid="follow-up-cell">${formatDay(followUp)}</span>
                                                    ${overdue ? html`
                                                        <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-bold uppercase tracking-wider text-rose-400 bg-rose-500/15 border border-rose-500/20" data-testid="follow-up-overdue">
                                                            ${icon(Clock, 10, 'w-2.5 h-2.5')} Overdue
                                                        </span>
                                                    ` : nothing}
                                                </div>
                                                ${quote.follow_up_owner_name ? html`
                                                    <div class="text-[10px] text-zinc-500 mt-0.5">${quote.follow_up_owner_name}</div>
                                                ` : nothing}
                                            ` : html`<span class="text-xs text-zinc-500" data-testid="follow-up-cell">None</span>`}
                                        </td>
                                        <td class="p-4">
                                            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border uppercase ${this.stateColors[quote.status] || ''}">
                                                ${quote.status}
                                            </span>
                                        </td>
                                        <td class="p-4 font-mono text-right text-gable-green">
                                            ${formatCents(quote.total_cents)}
                                        </td>
                                        <td class="p-4 text-right" @click=${(e: Event) => e.stopPropagation()}>
                                            <div class="flex items-center justify-end gap-1.5">
                                                ${quote.status === 'draft' ? html`
                                                    <button @click=${() => router.navigate(`/quotes/${quote.id}/edit`)} ?disabled=${isBusy}
                                                        class="text-amber-400 hover:text-amber-300 transition-colors p-1 rounded hover:bg-white/5 disabled:opacity-50" title="Edit Draft" aria-label="Edit draft">
                                                        ${icon(Pencil, 14)}
                                                    </button>
                                                ` : nothing}
                                                ${quote.status === 'draft' ? html`
                                                    <button @click=${() => this.handleStateChange(quote, 'sent')} ?disabled=${isBusy}
                                                        class="text-blue-400 hover:text-blue-300 transition-colors p-1 rounded hover:bg-white/5 disabled:opacity-50" title="Mark Sent" aria-label="Mark sent">
                                                        ${icon(Send, 14)}
                                                    </button>
                                                ` : nothing}
                                                ${(quote.status === 'draft' || quote.status === 'sent') ? html`
                                                    <button @click=${() => this.handleStateChange(quote, 'accepted')} ?disabled=${isBusy}
                                                        class="text-emerald-400 hover:text-emerald-300 transition-colors p-1 rounded hover:bg-white/5 disabled:opacity-50" title="Accept" aria-label="Accept quote">
                                                        ${icon(Check, 14)}
                                                    </button>
                                                    <button @click=${() => this.handleStateChange(quote, 'rejected')} ?disabled=${isBusy}
                                                        class="text-red-400 hover:text-red-300 transition-colors p-1 rounded hover:bg-white/5 disabled:opacity-50" title="Reject" aria-label="Reject quote">
                                                        ${icon(X, 14)}
                                                    </button>
                                                ` : nothing}
                                                ${(quote.status === 'draft' || quote.status === 'sent' || quote.status === 'accepted') ? html`
                                                    <button @click=${() => this.handleConvert(quote)} ?disabled=${isBusy}
                                                        class="text-gable-green hover:text-gable-green/80 transition-colors flex items-center gap-1 text-xs font-medium disabled:opacity-50 p-1 rounded hover:bg-white/5"
                                                        title="Convert to Order" aria-label="Convert to order">
                                                        ${icon(ShoppingCart, 14)}
                                                    </button>
                                                ` : nothing}
                                                <button @click=${() => router.navigate(`/quotes/${quote.id}`)}
                                                    class="text-white/50 hover:text-white transition-colors p-1 rounded hover:bg-white/5" aria-label="View quote details">
                                                    ${icon(ArrowRight, 14)}
                                                </button>
                                            </div>
                                        </td>
                                    </tr>
                                `;
                            })}
                        </tbody>
                    </table>
                </div>
                ${!this.loading && this.quotes.length > 0 ? html`
                    <div class="flex items-center justify-between text-sm text-zinc-500">
                        <span class="font-mono">${this.total !== null ? `${this.quotes.length} of ${this.total} quotes` : `${this.quotes.length} quotes`}</span>
                        ${this.nextCursor ? html`
                            <button @click=${() => this.loadMore()} ?disabled=${this.loadingMore}
                                class="border border-white/10 text-zinc-400 hover:text-white hover:border-white/20 font-medium px-4 py-2 rounded transition-colors text-sm disabled:opacity-50">
                                ${this.loadingMore ? 'Loading...' : 'Load more'}
                            </button>
                        ` : nothing}
                    </div>
                ` : nothing}
            </div>
        `;
    }
}
