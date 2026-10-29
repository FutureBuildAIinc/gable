// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import '../../components/ui/gable-select.ts';
import { customElement, property, state } from 'lit/decorators.js';
import { ToastService } from '../../lib/toast-service.ts';
import { formatDate } from '../../lib/dates.ts';
import { CustomerService } from '../../services/CustomerService.ts';
import { ApiError, apiErrorMessage, fieldErrorMap } from '../../services/apiError.ts';
import type { Alert, AlertKind, AlertRequest } from '../../types/customer.ts';
import { BUTTON_PRIMARY, BUTTON_QUIET, checkbox, idPrefix, labeled, textInput } from './formFields.ts';
import { confirmDialog } from '../../components/ui/app-dialog.ts';

interface AlertDraft {
    kind: AlertKind;
    text: string;
    show_at_counter: boolean;
    show_at_orders: boolean;
    acknowledge_required: boolean;
}

function emptyDraft(): AlertDraft {
    return {
        kind: 'note', text: '', show_at_counter: true, show_at_orders: false, acknowledge_required: false,
    };
}

/** The alerts of one customer: the office's notes and warnings for the counter and the order desk. */
@customElement('gable-alert-list')
export class GableAlertList extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) customerId = '';

    @state() private alerts: Alert[] = [];
    @state() private nextCursor: string | null = null;
    @state() private loading = true;
    @state() private error: string | null = null;
    @state() private adding = false;
    @state() private draft: AlertDraft = emptyDraft();
    @state() private saving = false;
    @state() private formError = '';
    @state() private fieldErrors: Record<string, string> = {};
    /** Whether the ended alerts are shown too. */
    @state() private showEnded = false;

    private readonly ids = idPrefix('alert');
    private loadedFor = '';

    connectedCallback() {
        super.connectedCallback();
        if (this.customerId) void this.fetchAlerts();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('customerId') && this.customerId && this.customerId !== this.loadedFor) void this.fetchAlerts();
    }

    private async fetchAlerts() {
        this.loadedFor = this.customerId;
        try {
            this.loading = true;
            const page = await CustomerService.listAlerts(this.customerId, {
                limit: 200,
                state: this.showEnded ? 'all' : 'open',
            });
            this.alerts = page.items;
            this.nextCursor = page.next_cursor;
            this.error = null;
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load alerts');
        } finally {
            this.loading = false;
        }
    }

    private async loadMore() {
        if (!this.nextCursor) return;
        try {
            const page = await CustomerService.listAlerts(this.customerId, {
                limit: 200, cursor: this.nextCursor, state: this.showEnded ? 'all' : 'open',
            });
            this.alerts = [...this.alerts, ...page.items];
            this.nextCursor = page.next_cursor;
        } catch (err) {
            ToastService.show(`Failed to load more: ${apiErrorMessage(err)}`, 'error');
        }
    }

    private openAdd() {
        this.adding = true;
        this.draft = emptyDraft();
        this.fieldErrors = {};
        this.formError = '';
    }

    private set<K extends keyof AlertDraft>(key: K, value: AlertDraft[K]) {
        this.draft = { ...this.draft, [key]: value };
    }

    /** The kind drives the flags: only a warning may ask to be acknowledged. */
    private setKind(kind: AlertKind) {
        this.draft = { ...this.draft, kind, acknowledge_required: kind === 'warning' ? this.draft.acknowledge_required : false };
    }

    private async save(e: Event) {
        e.preventDefault();
        const d = this.draft;
        const request: AlertRequest = {
            kind: d.kind,
            text: d.text.trim(),
            show_at_counter: d.show_at_counter,
            show_at_orders: d.show_at_orders,
            acknowledge_required: d.kind === 'warning' ? d.acknowledge_required : false,
        };
        this.saving = true;
        this.fieldErrors = {};
        this.formError = '';
        try {
            await CustomerService.addAlert(this.customerId, request);
            ToastService.show(d.kind === 'warning' ? 'Warning added' : 'Note added', 'success');
            this.adding = false;
            await this.fetchAlerts();
        } catch (err) {
            this.fieldErrors = fieldErrorMap(err);
            this.formError = apiErrorMessage(err, 'Failed to add alert');
        } finally {
            this.saving = false;
        }
    }

    /** An alert is ended, not deleted: the row stays and says who called it off. */
    private async handleEnd(a: Alert) {
        if (!await confirmDialog({
            title: 'End this alert?',
            body: 'It stops speaking at the counter and the order desk. The record of it stays on the account.',
            confirmLabel: 'End alert',
        })) return;
        try {
            await CustomerService.endAlert(this.customerId, a.id, a.revision);
            ToastService.show('Alert ended', 'success');
        } catch (err) {
            if (err instanceof ApiError && err.isStaleRevision) {
                ToastService.show(`${err.message} The alerts were reloaded.`, 'error');
            } else {
                ToastService.show(apiErrorMessage(err, 'Failed to end alert'), 'error');
            }
        }
        await this.fetchAlerts();
    }

    private renderForm() {
        const id = (n: string) => `${this.ids}-${n}`;
        const d = this.draft;
        const err = this.fieldErrors;
        const isWarning = d.kind === 'warning';
        return html`
            <form @submit=${(e: Event) => void this.save(e)} class="mb-6 p-5 bg-slate-steel border border-white/10 rounded-2xl space-y-4" aria-label="Add alert" data-testid="alert-form">
                ${this.formError ? html`<div class="text-sm text-red-400 whitespace-pre-line" role="alert">${this.formError}</div>` : nothing}
                <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                    ${labeled(
                        id('kind'),
                        'Kind',
                        html`<gable-select id=${id('kind')} .value=${d.kind} .options=${[{ value: 'note', label: 'Note' }, { value: 'warning', label: 'Warning' }]} @change=${(e: Event) => this.setKind((e.target as HTMLSelectElement).value as AlertKind)}></gable-select>`,
                        err.kind,
                    )}
                    ${labeled(
                        id('text'),
                        'Alert',
                        textInput(id('text'), d.text, v => this.set('text', v), { required: true, maxlength: 500, placeholder: 'What the counter or the order desk should know' }),
                        err.text,
                        'At most 500 characters.',
                    )}
                </div>
                <div class="flex flex-wrap items-center gap-6">
                    ${checkbox(id('counter'), 'Show at the counter', d.show_at_counter, v => this.set('show_at_counter', v))}
                    ${checkbox(id('orders'), 'Show on orders', d.show_at_orders, v => this.set('show_at_orders', v))}
                    ${isWarning ? checkbox(id('ack'), 'The counter must acknowledge it before the sale', d.acknowledge_required, v => this.set('acknowledge_required', v)) : nothing}
                    <div class="ml-auto flex gap-2">
                        <button type="button" class=${BUTTON_QUIET} @click=${() => { this.adding = false; }}>Cancel</button>
                        <button type="submit" ?disabled=${this.saving} class=${BUTTON_PRIMARY} data-testid="alert-save">${this.saving ? 'Saving...' : 'Add alert'}</button>
                    </div>
                </div>
            </form>
        `;
    }

    private renderAlert(a: Alert) {
        const isWarning = a.kind === 'warning';
        const ended = a.ended_at !== null;
        return html`
            <tr class="${ended ? 'opacity-60' : ''}" data-testid="alert-row" data-alert-kind=${a.kind}>
                <td class="px-4 py-3 text-white">
                    <span class="px-2 py-0.5 rounded text-xs font-medium ${isWarning ? 'bg-amber-500/10 text-amber-400 border border-amber-500/20' : 'bg-blue-500/10 text-blue-400 border border-blue-500/20'}">${isWarning ? 'Warning' : 'Note'}</span>
                    ${a.acknowledge_required ? html`<span class="ml-2 px-2 py-0.5 rounded text-xs font-medium bg-red-500/10 text-red-400 border border-red-500/20">Must be acknowledged</span>` : nothing}
                    <div class="mt-1" data-testid="alert-text">${a.text}</div>
                </td>
                <td class="px-4 py-3 text-zinc-400">
                    ${a.show_at_counter ? html`<div data-testid="alert-at-counter">At the counter</div>` : nothing}
                    ${a.show_at_orders ? html`<div data-testid="alert-at-orders">On orders</div>` : nothing}
                    ${!a.show_at_counter && !a.show_at_orders ? html`<span class="text-zinc-600">Nowhere yet</span>` : nothing}
                </td>
                <td class="px-4 py-3 text-zinc-400">
                    <div>${a.created_by}</div>
                    <div class="text-xs text-zinc-500">${formatDate(a.created_at)}</div>
                    ${ended ? html`<div class="text-xs text-zinc-500" data-testid="alert-ended">Ended by ${a.ended_by ?? 'unknown'}</div>` : nothing}
                </td>
                <td class="px-4 py-3 text-right whitespace-nowrap">
                    ${ended ? nothing : html`<button class="text-zinc-300 hover:text-white" @click=${() => void this.handleEnd(a)} data-testid="alert-end">End</button>`}
                </td>
            </tr>
        `;
    }

    render() {
        if (this.loading && this.alerts.length === 0) return html`<div class="text-zinc-400">Loading alerts...</div>`;
        if (this.error) return html`<div class="text-red-400" role="alert">${this.error}</div>`;

        return html`
            <div>
                <div class="flex justify-between items-center mb-6">
                    <div>
                        <h3 class="text-lg font-semibold text-white">Alerts</h3>
                        <p class="text-zinc-500 text-sm">What the counter and the order desk should know about this account</p>
                    </div>
                    <div class="flex items-center gap-3">
                        <label class="flex items-center gap-2 text-sm text-zinc-300">
                            <input
                                type="checkbox"
                                .checked=${this.showEnded}
                                @change=${(e: Event) => { this.showEnded = (e.target as HTMLInputElement).checked; void this.fetchAlerts(); }}
                                class="h-4 w-4 rounded border-white/20 bg-slate-steel/50"
                                data-testid="alert-show-ended"
                            />
                            Show ended
                        </label>
                        <button class=${BUTTON_PRIMARY} @click=${() => (this.adding ? (this.adding = false) : this.openAdd())} data-testid="alert-add">
                            ${this.adding ? 'Cancel' : 'Add alert'}
                        </button>
                    </div>
                </div>

                ${this.adding ? this.renderForm() : nothing}

                ${this.alerts.length === 0
                    ? html`<div class="text-center py-8 text-zinc-500 border border-dashed border-white/10 rounded-lg" data-testid="alert-empty">No alerts on this account.</div>`
                    : html`
                        <div class="border border-white/5 rounded-lg overflow-hidden bg-slate-steel/20">
                            <table class="w-full text-sm">
                                <thead class="bg-white/5 text-zinc-400 font-medium border-b border-white/5">
                                    <tr>
                                        <th scope="col" class="px-4 py-3 text-left">Alert</th>
                                        <th scope="col" class="px-4 py-3 text-left">Where it shows</th>
                                        <th scope="col" class="px-4 py-3 text-left">Left by</th>
                                        <th scope="col" class="px-4 py-3 text-right"><span class="sr-only">Actions</span></th>
                                    </tr>
                                </thead>
                                <tbody class="divide-y divide-white/5">
                                    ${this.alerts.map(a => this.renderAlert(a))}
                                </tbody>
                            </table>
                        </div>
                    `}

                ${this.nextCursor ? html`<div class="flex justify-center mt-4"><button class=${BUTTON_QUIET} @click=${() => void this.loadMore()}>Load more</button></div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-alert-list': GableAlertList;
    }
}
