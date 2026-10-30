// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { offlineAdminService } from '../../services/OfflineAdminService.ts';
import type { ExceptionResolution, OfflineException } from '../../services/OfflineAdminService.ts';
import { ApiError, apiErrorMessage } from '../../services/apiError.ts';
import { branchContext } from '../../services/BranchContext.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import { formatDay } from '../../lib/dates.ts';
import { reasonWords, resolutionsFor } from './offlineExceptions.ts';

/**
 * The branch's offline exceptions (ADR 0011 section 3.4), under the end of day
 * on the Daily Till page: the sales a register made offline that the server
 * could not post or verify. A manager settles each one; the till close and the
 * day close wait for the list to empty. Owner and admin only: a user the server
 * refuses sees nothing.
 */
@customElement('gable-offline-exceptions')
export class GableOfflineExceptions extends LitElement {
    createRenderRoot() { return this; }

    @state() private items: OfflineException[] = [];
    @state() private loading = true;
    @state() private forbidden = false;
    @state() private failure = '';
    @state() private busy = false;
    /** The exception being settled, the way chosen and the reason typed. */
    @state() private openId = '';
    @state() private way: ExceptionResolution | '' = '';
    @state() private reason = '';

    private unsubscribe: (() => void) | null = null;
    private seq = 0;

    connectedCallback() {
        super.connectedCallback();
        this.unsubscribe = onBranchChanged(() => { void this.load(); });
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
        this.openId = '';
        if (!id) {
            this.items = [];
            this.loading = false;
            return;
        }
        this.loading = true;
        try {
            const items = await offlineAdminService.exceptions(id);
            if (seq === this.seq) this.items = items;
        } catch (err: unknown) {
            if (seq !== this.seq) return;
            this.items = [];
            this.forbidden = err instanceof ApiError && (err.status === 403 || err.status === 404);
            this.failure = apiErrorMessage(err, 'Failed to load the offline exceptions');
        } finally {
            if (seq === this.seq) this.loading = false;
        }
    }

    private begin(item: OfflineException) {
        this.openId = item.id;
        this.way = resolutionsFor(item.actions)[0]?.value ?? '';
        this.reason = '';
        this.failure = '';
    }

    private async settle(item: OfflineException) {
        const choice = resolutionsFor(item.actions).find((c) => c.value === this.way);
        if (!choice) return;
        const reason = this.reason.trim();
        if (choice.needsReason && reason === '') {
            this.failure = 'Say why, so the audit row can name it.';
            return;
        }
        try {
            this.busy = true;
            this.failure = '';
            await offlineAdminService.resolveException(item, choice.value, reason);
            this.items = this.items.filter((i) => i.id !== item.id);
            this.openId = '';
            ToastService.show(`${item.offline_ref || 'The sale'}: ${choice.label.toLowerCase()} done`, 'success');
        } catch (err: unknown) {
            // a refusal (a manager other than the cashier, a blocker) is said in the server's words and the item stays
            this.failure = apiErrorMessage(err, 'Failed to resolve the exception');
        } finally {
            this.busy = false;
        }
    }

    private renderForm(item: OfflineException) {
        const choices = resolutionsFor(item.actions);
        const picked = choices.find((c) => c.value === this.way) ?? choices[0];
        if (!picked) return nothing;
        return html`
            <div class="mt-3 p-3 bg-black/20 rounded border border-white/5 space-y-3" data-testid="offline-exception-form">
                <div class="flex flex-wrap gap-4" role="radiogroup" aria-label="How to settle it">
                    ${choices.map((c) => html`
                        <label class="flex items-center gap-2 text-sm text-zinc-200">
                            <input type="radio" name="resolution-${item.id}" data-testid="offline-way-${c.value}"
                                .checked=${picked.value === c.value}
                                @change=${() => { this.way = c.value; this.failure = ''; }} />
                            ${c.label}
                        </label>`)}
                </div>
                <p class="text-xs text-zinc-400" data-testid="offline-way-help">${picked.help}</p>
                <input type="text" data-testid="offline-reason" maxlength="200" .value=${this.reason}
                    placeholder=${picked.needsReason ? 'Why (goes on the audit row)' : 'Note (optional)'}
                    @input=${(e: Event) => { this.reason = (e.target as HTMLInputElement).value; }}
                    class="w-full px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white text-sm" />
                <div class="flex gap-3">
                    <button type="button" data-testid="offline-resolve" @click=${() => this.settle(item)} ?disabled=${this.busy}
                        class="px-4 py-2 rounded bg-emerald-500 text-black text-sm font-bold disabled:opacity-40">${picked.label}</button>
                    <button type="button" data-testid="offline-cancel" @click=${() => { this.openId = ''; this.failure = ''; }}
                        class="px-4 py-2 rounded border border-zinc-700 text-zinc-200 text-sm">Cancel</button>
                </div>
            </div>`;
    }

    render() {
        if (this.forbidden || !branchContext.currentId) return nothing;
        if (this.loading && this.items.length === 0) return nothing;
        if (this.items.length === 0 && !this.failure) {
            return html`
                <div class="mt-6 bg-zinc-900 border border-zinc-800 rounded-lg p-6" data-testid="offline-exceptions">
                    <h2 class="text-xl font-bold text-emerald-400">Offline exceptions</h2>
                    <p class="mt-2 text-zinc-500" data-testid="offline-exceptions-none">No offline sale is waiting for a manager.</p>
                </div>`;
        }
        return html`
            <div class="mt-6 bg-zinc-900 border border-zinc-800 rounded-lg p-6" data-testid="offline-exceptions">
                <div class="flex items-center justify-between gap-4 mb-2">
                    <h2 class="text-xl font-bold text-amber-500">Offline exceptions
                        <span class="font-mono" data-testid="offline-exceptions-count">${this.items.length}</span></h2>
                </div>
                <p class="text-sm text-zinc-400 mb-4">A register sold these while offline and the server could not post them. The drawer and the day cannot close until each is settled.</p>
                <ul class="space-y-3" data-testid="offline-exceptions-list">
                    ${this.items.map((item) => html`
                        <li class="p-4 bg-black/20 rounded border border-white/5" data-testid="offline-exception-${item.id}">
                            <div class="flex flex-wrap items-center justify-between gap-3">
                                <div>
                                    <p class="text-white font-bold" data-testid="offline-exception-reason">${reasonWords(item.reason_code)}</p>
                                    <p class="text-sm text-zinc-400">
                                        <span class="font-mono text-zinc-200">${item.offline_ref}</span>
                                        ${item.offline_ref ? html`on ` : nothing}${item.register_id}, ${formatDay(item.business_date)}
                                        ${item.status === 'held' ? html`<span class="text-amber-500">(held until its link arrives)</span>` : nothing}
                                    </p>
                                    ${item.reason_message ? html`<p class="text-sm text-zinc-500">${item.reason_message}</p>` : nothing}
                                </div>
                                <div class="flex items-center gap-4">
                                    <span class="font-mono text-white text-lg" data-testid="offline-exception-total">${formatCents(item.total_cents)}</span>
                                    ${this.openId === item.id ? nothing : html`
                                        <button type="button" data-testid="offline-settle" @click=${() => this.begin(item)}
                                            class="px-4 py-2 rounded border border-zinc-700 text-zinc-200 text-sm font-bold">Settle</button>`}
                                </div>
                            </div>
                            ${this.openId === item.id ? this.renderForm(item) : nothing}
                        </li>`)}
                </ul>
                ${this.failure ? html`<p class="mt-4 text-red-400 text-sm" role="alert" data-testid="offline-exceptions-error">${this.failure}</p>` : nothing}
            </div>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-offline-exceptions': GableOfflineExceptions;
    }
}
