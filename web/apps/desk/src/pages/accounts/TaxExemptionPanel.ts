// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { FileCheck, Plus, X } from 'lucide';
import { ToastService } from '../../lib/toast-service.ts';
import { apiErrorMessage } from '../../services/apiError.ts';
import { TaxExemptionService } from '../../services/TaxExemptionService.ts';
import { confirmDialog } from '../../components/ui/app-dialog.ts';
import type { TaxExemption } from '../../types/tax';
import { labeled, textInput } from './formFields.ts';

/** The certificate's status words, as a clerk reads them. */
export function exemptionStatus(ex: TaxExemption, today: string): string {
    if (!ex.is_active) return 'Ended';
    const from = ex.effective_date.slice(0, 10);
    if (from > today) return 'Not in effect yet';
    if (ex.expiry_date && ex.expiry_date.slice(0, 10) < today) return 'Expired';
    return 'Active';
}

/** True when the certificate exempts a sale today (the counter's own rule). */
export function exemptionCoversToday(ex: TaxExemption, today: string): boolean {
    if (!ex.is_active) return false;
    if (ex.effective_date.slice(0, 10) > today) return false;
    if (ex.expiry_date && ex.expiry_date.slice(0, 10) < today) return false;
    return true;
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;
const today = () => new Date().toISOString().slice(0, 10);

interface ExemptionDraft {
    certificate_number: string;
    exempt_reason: string;
    issuing_state: string;
    effective_date: string;
    expiry_date: string;
}

const emptyDraft = (): ExemptionDraft => ({
    certificate_number: '', exempt_reason: '', issuing_state: '', effective_date: today(), expiry_date: '',
});

/** The dialog's own validation, in words beside the field it refused. */
export function draftErrors(d: ExemptionDraft): Record<string, string> {
    const errs: Record<string, string> = {};
    if (!d.certificate_number.trim()) errs.certificate_number = 'The certificate number is required.';
    if (!d.exempt_reason.trim()) errs.exempt_reason = 'The reason is required (for example RESALE).';
    if (d.issuing_state.trim() && !/^[A-Za-z]{2}$/.test(d.issuing_state.trim())) {
        errs.issuing_state = 'The issuing state or province is two letters.';
    }
    if (!DAY_RE.test(d.effective_date)) errs.effective_date = 'The effective day is a date, YYYY-MM-DD.';
    if (d.expiry_date && !DAY_RE.test(d.expiry_date)) errs.expiry_date = 'The expiry day is a date, YYYY-MM-DD.';
    if (!errs.effective_date && !errs.expiry_date && d.expiry_date && d.expiry_date < d.effective_date) {
        errs.expiry_date = 'The expiry day is on or after the effective day.';
    }
    return errs;
}

/**
 * The account's tax exemption certificates: the ones on file (number,
 * reason, issuing state, effective, expiry, active or expired), Add
 * certificate (a dialog with those fields, validated) and End certificate
 * (inactive, with a confirm).
 */
@customElement('gable-tax-exemption-panel')
export class GableTaxExemptionPanel extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) customerId = '';

    @state() private exemptions: TaxExemption[] = [];
    @state() private loading = true;
    @state() private error: string | null = null;
    @state() private dialogOpen = false;
    @state() private draft: ExemptionDraft = emptyDraft();
    @state() private fieldErrors: Record<string, string> = {};
    @state() private saving = false;

    connectedCallback() {
        super.connectedCallback();
        if (this.customerId) void this.load();
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('customerId') && this.customerId) void this.load();
    }

    private async load() {
        try {
            this.loading = true;
            this.error = null;
            this.exemptions = await TaxExemptionService.list(this.customerId);
        } catch (err) {
            this.error = apiErrorMessage(err, 'Failed to load tax exemptions');
        } finally {
            this.loading = false;
        }
    }

    private _set<K extends keyof ExemptionDraft>(key: K, value: string) {
        this.draft = { ...this.draft, [key]: value };
        if (this.fieldErrors[key]) this.fieldErrors = { ...this.fieldErrors, [key]: '' };
    }

    private async _save() {
        const errs = draftErrors(this.draft);
        if (Object.keys(errs).length > 0) {
            this.fieldErrors = errs;
            return;
        }
        this.saving = true;
        try {
            await TaxExemptionService.create({
                customer_id: this.customerId,
                certificate_number: this.draft.certificate_number.trim(),
                exempt_reason: this.draft.exempt_reason.trim(),
                issuing_state: this.draft.issuing_state.trim().toUpperCase(),
                effective_date: this.draft.effective_date,
                expiry_date: this.draft.expiry_date || undefined,
            });
            ToastService.show('Certificate recorded', 'success');
            this.dialogOpen = false;
            this.draft = emptyDraft();
            this.fieldErrors = {};
            await this.load();
        } catch (err) {
            ToastService.show(apiErrorMessage(err, 'Failed to record the certificate'), 'error');
        } finally {
            this.saving = false;
        }
    }

    private async _end(ex: TaxExemption) {
        const ok = await confirmDialog({
            title: 'End this certificate?',
            body: `Certificate ${ex.certificate_number || '(no number)'} stays on file but no longer exempts this account's sales.`,
            confirmLabel: 'End certificate',
            tone: 'danger',
        });
        if (!ok) return;
        try {
            await TaxExemptionService.end(ex.id);
            ToastService.show('Certificate ended', 'success');
            await this.load();
        } catch (err) {
            ToastService.show(apiErrorMessage(err, 'Failed to end the certificate'), 'error');
        }
    }

    render() {
        const day = today();
        return html`
            <div class="space-y-4">
                <div class="flex items-center justify-between">
                    <div>
                        <h2 class="text-lg font-semibold text-white flex items-center gap-2">
                            <span class="text-gable-green">${icon(FileCheck, 20)}</span>
                            Tax exemption
                        </h2>
                        <p class="text-sm text-zinc-500 mt-1">The account's exemption certificates. An active certificate exempts its sales from tax.</p>
                    </div>
                    <button data-testid="tax-exemption-add" @click=${() => { this.dialogOpen = true; }}
                        class="flex items-center gap-2 px-4 py-2 bg-gable-green text-black rounded-lg text-sm font-semibold hover:shadow-glow transition-all">
                        ${icon(Plus, 16)} Add certificate
                    </button>
                </div>

                ${this.error ? html`
                    <div class="bg-safety-red/10 border border-safety-red/30 text-safety-red text-sm px-4 py-3 rounded-lg">${this.error}</div>
                ` : nothing}

                <div class="bg-slate-steel border border-white/5 rounded-xl overflow-hidden" data-testid="tax-exemption-panel">
                    ${this.loading ? html`
                        <div class="p-12 text-center text-zinc-500 text-sm">Loading...</div>
                    ` : this.exemptions.length === 0 ? html`
                        <div class="p-12 text-center text-zinc-500 text-sm" data-testid="tax-exemption-empty">No certificates on file.</div>
                    ` : html`
                        <table class="w-full text-sm">
                            <thead class="bg-deep-space/40 text-xs uppercase tracking-wider text-zinc-500">
                                <tr>
                                    <th class="text-left px-4 py-3 font-semibold">Certificate</th>
                                    <th class="text-left px-4 py-3 font-semibold">Reason</th>
                                    <th class="text-left px-4 py-3 font-semibold">State</th>
                                    <th class="text-left px-4 py-3 font-semibold">Effective</th>
                                    <th class="text-left px-4 py-3 font-semibold">Expiry</th>
                                    <th class="text-left px-4 py-3 font-semibold">Status</th>
                                    <th class="text-right px-4 py-3 font-semibold"></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-white/5">
                                ${this.exemptions.map((ex) => {
                                    const status = exemptionStatus(ex, day);
                                    return html`
                                    <tr data-testid="tax-exemption-row">
                                        <td class="px-4 py-3 font-mono text-gable-green">${ex.certificate_number || '—'}</td>
                                        <td class="px-4 py-3 text-zinc-300">${ex.exempt_reason}</td>
                                        <td class="px-4 py-3 text-zinc-400">${ex.issuing_state || '—'}</td>
                                        <td class="px-4 py-3 text-zinc-400 font-mono text-xs">${ex.effective_date.slice(0, 10)}</td>
                                        <td class="px-4 py-3 text-zinc-400 font-mono text-xs">${ex.expiry_date ? ex.expiry_date.slice(0, 10) : 'No expiry'}</td>
                                        <td class="px-4 py-3">
                                            <span class="text-xs px-2 py-0.5 rounded-full font-semibold ${exemptionCoversToday(ex, day)
                                                ? 'bg-gable-green/15 text-gable-green' : 'bg-zinc-700/40 text-zinc-400'}"
                                                data-testid="tax-exemption-status">${status}</span>
                                        </td>
                                        <td class="px-4 py-3 text-right">
                                            ${ex.is_active ? html`
                                                <button data-testid="tax-exemption-end" @click=${() => { void this._end(ex); }}
                                                    class="px-3 py-1.5 border border-white/10 text-zinc-300 hover:text-safety-red text-sm rounded-md transition-colors">End</button>
                                            ` : nothing}
                                        </td>
                                    </tr>`;
                                })}
                            </tbody>
                        </table>
                    `}
                </div>

                ${this.dialogOpen ? this._renderDialog() : nothing}
            </div>
        `;
    }

    private _renderDialog() {
        const ids = { number: 'tax-exempt-number', reason: 'tax-exempt-reason', state: 'tax-exempt-state',
            effective: 'tax-exempt-effective', expiry: 'tax-exempt-expiry' };
        return html`
            <div class="fixed inset-0 z-50 bg-deep-space/80 backdrop-blur-sm flex items-center justify-center p-4"
                @click=${() => { if (!this.saving) this.dialogOpen = false; }}
                data-testid="tax-exemption-dialog">
                <div class="bg-slate-steel border border-white/10 rounded-xl w-full max-w-lg shadow-elevation-2"
                    @click=${(e: MouseEvent) => e.stopPropagation()}>
                    <div class="flex items-center justify-between px-6 py-4 border-b border-white/5">
                        <h3 class="text-lg font-semibold text-white">Add certificate</h3>
                        <button @click=${() => { this.dialogOpen = false; }} class="p-1 rounded hover:bg-white/5 text-zinc-400 hover:text-white"
                            aria-label="Close">${icon(X, 16)}</button>
                    </div>
                    <div class="p-6 grid grid-cols-1 md:grid-cols-2 gap-4">
                        <div class="md:col-span-2">${labeled(ids.number, 'Certificate number *',
                            textInput(ids.number, this.draft.certificate_number, (v) => this._set('certificate_number', v),
                                { placeholder: 'CERT-4471', mono: true }), this.fieldErrors.certificate_number)}</div>
                        <div>${labeled(ids.reason, 'Reason *',
                            textInput(ids.reason, this.draft.exempt_reason, (v) => this._set('exempt_reason', v),
                                { placeholder: 'RESALE' }), this.fieldErrors.exempt_reason)}</div>
                        <div>${labeled(ids.state, 'Issuing state or province',
                            textInput(ids.state, this.draft.issuing_state, (v) => this._set('issuing_state', v),
                                { placeholder: 'CT', maxlength: 2 }), this.fieldErrors.issuing_state)}</div>
                        <div>${labeled(ids.effective, 'Effective day *',
                            textInput(ids.effective, this.draft.effective_date, (v) => this._set('effective_date', v),
                                { placeholder: 'YYYY-MM-DD', mono: true }), this.fieldErrors.effective_date)}</div>
                        <div>${labeled(ids.expiry, 'Expiry day',
                            textInput(ids.expiry, this.draft.expiry_date, (v) => this._set('expiry_date', v),
                                { placeholder: 'YYYY-MM-DD', mono: true }), this.fieldErrors.expiry_date, 'Leave empty when the certificate does not expire.')}</div>
                    </div>
                    <div class="px-6 py-4 border-t border-white/5 flex items-center justify-end gap-3">
                        <button @click=${() => { this.dialogOpen = false; }} ?disabled=${this.saving}
                            class="px-4 py-2 text-sm text-zinc-300 hover:bg-white/5 rounded-lg transition-colors">Cancel</button>
                        <button data-testid="tax-exemption-save" @click=${() => { void this._save(); }} ?disabled=${this.saving}
                            class="px-4 py-2 text-sm font-semibold bg-gable-green text-black rounded-lg hover:shadow-glow transition-all disabled:opacity-50">
                            ${this.saving ? 'Saving...' : 'Record certificate'}</button>
                    </div>
                </div>
            </div>
        `;
    }
}
