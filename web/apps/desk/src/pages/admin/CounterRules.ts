// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ShieldCheck } from 'lucide';
import { apiErrorMessage, fieldErrorMap } from '../../services/apiError';
import { posService, dollarsToCents } from '../../services/POSService';
import type { PosSettings } from '../../types/pos.ts';

interface Draft {
    /** The database wide check limit in dollars text; empty is no limit. */
    limit: string;
    /** The ID rule's choice: never, every check, or checks over a figure. */
    idMode: 'never' | 'every_check' | 'over_amount';
    /** The figure an over_amount rule names, in dollars text. */
    idOver: string;
}

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green';

/** The dollars text a stored cents figure shows; null is empty. */
function dollarsText(cents: number | null): string {
    if (cents === null) return '';
    const sign = cents < 0 ? '-' : '';
    const abs = Math.abs(cents);
    return `${sign}${Math.trunc(abs / 100)}.${String(abs % 100).padStart(2, '0')}`;
}

/** The draft as rules on the wire, or a field error. */
function draftRules(d: Draft): { ok: true; rules: PosSettings['rules'] } | { ok: false; message: string } {
    let check_limit_cents: number | null = null;
    if (d.limit.trim() !== '') {
        const cents = dollarsToCents(d.limit.trim());
        if (isNaN(cents) || cents < 0) return { ok: false, message: 'The check limit must be a dollar amount, or empty for no limit' };
        check_limit_cents = cents;
    }
    let over_cents: number | null = null;
    if (d.idMode === 'over_amount') {
        const cents = dollarsToCents(d.idOver.trim());
        if (d.idOver.trim() === '' || isNaN(cents) || cents <= 0) {
            return { ok: false, message: 'Name the check amount the ID rule starts at, more than zero' };
        }
        over_cents = cents;
    }
    return { ok: true, rules: { check_limit_cents, check_id_required: { mode: d.idMode, over_cents } } };
}

/**
 * The dealer's counter rules (CP-04): the database wide check limit and the
 * check ID rule. Owner and admin only (the write is theirs on the server).
 */
@customElement('gable-counter-rules')
export class GableCounterRules extends LitElement {
    createRenderRoot() { return this; }

    @state() private _settings: PosSettings | null = null;
    @state() private _draft: Draft | null = null;
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;
    @state() private _fieldErrors: Record<string, string> = {};
    @state() private _success: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private _adopt(s: PosSettings) {
        this._settings = s;
        this._draft = {
            limit: dollarsText(s.rules.check_limit_cents),
            idMode: s.rules.check_id_required.mode,
            idOver: dollarsText(s.rules.check_id_required.over_cents),
        };
    }

    private async _load() {
        this._loading = true;
        try {
            this._adopt(await posService.settings());
        } catch (err) {
            this._error = apiErrorMessage(err, 'Could not read the counter rules');
        } finally {
            this._loading = false;
        }
    }

    private async _save() {
        const d = this._draft;
        if (d === null || this._settings === null || this._saving) return;
        this._error = null;
        this._success = null;
        this._fieldErrors = {};
        const rules = draftRules(d);
        if (!rules.ok) {
            this._error = rules.message;
            return;
        }
        this._saving = true;
        try {
            this._adopt(await posService.saveSettings(rules.rules, this._settings.revision));
            this._success = 'Counter rules saved';
        } catch (err) {
            this._fieldErrors = fieldErrorMap(err);
            this._error = apiErrorMessage(err, 'Could not save the counter rules');
        } finally {
            this._saving = false;
        }
    }

    render() {
        if (this._loading) return html`<div class="text-slate-400 p-8">Loading counter rules...</div>`;
        if (!this._settings || !this._draft) {
            return html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">${this._error}</div>`;
        }
        const d = this._draft;
        const idChoices: { value: Draft['idMode']; label: string }[] = [
            { value: 'never', label: 'Never ask for an ID' },
            { value: 'every_check', label: 'Every check needs the customer\'s ID' },
            { value: 'over_amount', label: 'Checks over an amount need the customer\'s ID' },
        ];
        return html`
            <div class="space-y-6">
                <div>
                    <h2 class="text-xl font-bold text-white flex items-center gap-2">
                        ${icon(ShieldCheck, 20, 'w-5 h-5 text-gable-green')}
                        Counter rules
                    </h2>
                    <p class="text-slate-400 text-sm mt-1">
                        The rules every register of the dealer holds a check to. A customer's own check limit wins over the
                        database wide one. Only the kind of ID is ever kept, never its number.
                    </p>
                </div>

                <div class="bg-slate-steel border border-white/10 rounded-lg p-6 space-y-5">
                    <label class="block space-y-1">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Check limit (dollars)</span>
                        <input type="text" inputmode="decimal" name="check-limit" data-testid="counter-rules-limit"
                            placeholder="Empty means no limit" .value=${d.limit}
                            @input=${(e: Event) => { this._draft = { ...d, limit: (e.target as HTMLInputElement).value }; }}
                            class="${FIELD_CLASS} font-mono" />
                        ${this._fieldErrors['rules.check_limit_cents']
                            ? html`<span class="text-xs text-red-400">${this._fieldErrors['rules.check_limit_cents']}</span>` : nothing}
                        <span class="block text-xs text-slate-500">The most any one check may be written for, database wide, unless the customer's account carries its own limit.</span>
                    </label>

                    <fieldset class="space-y-2">
                        <legend class="text-xs uppercase tracking-wider text-slate-400">Check ID rule</legend>
                        ${idChoices.map((c) => html`
                            <label class="flex items-center gap-2 text-sm text-slate-200" data-testid="counter-rules-id-choice-${c.value}">
                                <input type="radio" name="id-mode" data-testid="counter-rules-id-mode" .value=${c.value}
                                    .checked=${d.idMode === c.value}
                                    @change=${() => { this._draft = { ...d, idMode: c.value }; }} />
                                ${c.label}
                            </label>`)}
                        ${d.idMode === 'over_amount' ? html`
                            <div class="pl-6">
                                <label class="block space-y-1">
                                    <span class="text-xs uppercase tracking-wider text-slate-400">Checks over (dollars)</span>
                                    <input type="text" inputmode="decimal" name="id-over" data-testid="counter-rules-id-over"
                                        placeholder="50.00" .value=${d.idOver}
                                        @input=${(e: Event) => { this._draft = { ...d, idOver: (e.target as HTMLInputElement).value }; }}
                                        class="${FIELD_CLASS} font-mono max-w-48" />
                                    ${this._fieldErrors['rules.check_id_required.over_cents']
                                        ? html`<span class="text-xs text-red-400">${this._fieldErrors['rules.check_id_required.over_cents']}</span>` : nothing}
                                </label>
                            </div>` : nothing}
                    </fieldset>

                    <div class="flex justify-end">
                        <button type="button" data-testid="counter-rules-save" @click=${() => void this._save()} ?disabled=${this._saving}
                            class="inline-flex items-center gap-2 bg-gable-green text-black font-semibold px-4 py-2 rounded disabled:opacity-50">
                            ${this._saving ? 'Saving...' : 'Save'}
                        </button>
                    </div>
                </div>

                ${this._error ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" data-testid="counter-rules-error">${this._error}</div>` : nothing}
                ${this._success ? html`<div class="bg-emerald-500/10 border border-emerald-500/20 rounded-lg p-3 text-sm text-emerald-400" data-testid="counter-rules-saved">${this._success}</div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-counter-rules': GableCounterRules;
    }
}
