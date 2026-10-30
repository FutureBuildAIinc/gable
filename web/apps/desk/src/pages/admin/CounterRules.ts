// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ShieldCheck } from 'lucide';
import { apiErrorMessage, fieldErrorMap } from '../../services/apiError';
import { posService, dollarsToCents } from '../../services/POSService';
import type { PosSettings, ReturnReason } from '../../types/pos.ts';
import '../../components/ui/gable-select.ts';

interface DraftTier {
    /** The base tier the tier covers: any base, the ordinary one or the deepest. */
    baseTier: '' | 'flat' | 'deep';
    /** The lowest colorant total the tier covers, in fluid ounces text; empty is any total. */
    minOunces: string;
    /** The charge for a tint the tier covers, in dollars text. */
    charge: string;
}

interface Draft {
    /** The database wide check limit in dollars text; empty is no limit. */
    limit: string;
    /** The ID rule's choice: never, every check, or checks over a figure. */
    idMode: 'never' | 'every_check' | 'over_amount';
    /** The figure an over_amount rule names, in dollars text. */
    idOver: string;
    /** Each return reason's restocking fee in percent text; empty is none. */
    fees: Record<string, string>;
    /** The tint charge per container in dollars text; empty is no charge. */
    tintFlat: string;
    /** The dealer's tint tiers. */
    tintTiers: DraftTier[];
    /** The tinted no return policy: a tinted can is not taken back. */
    tintedNoReturn: boolean;
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

/** Basis points as percent text for an input: 1500 is 15, 1250 is 12.5, 5 is 0.05. */
export function bpsToPercent(bps: number): string {
    const whole = Math.floor(bps / 100);
    const frac = bps % 100;
    return frac === 0 ? `${whole}` : `${whole}.${String(frac).padStart(2, '0')}`;
}

/** Percent text as basis points, or null when it is not a percent from 0 to 100. */
export function percentToBps(text: string): number | null {
    const t = text.trim();
    if (t === '') return 0;
    if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
    const [whole, frac = ''] = t.split('.');
    const bps = Number(whole) * 100 + Number(frac.padEnd(2, '0'));
    return bps > 10000 ? null : bps;
}

/** The draft as rules on the wire, or a field error. */
function draftRules(d: Draft, reasons: ReturnReason[]): { ok: true; rules: PosSettings['rules'] } | { ok: false; message: string } {
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
    const restocking_fees: Record<string, number> = {};
    for (const r of reasons) {
        const bps = percentToBps(d.fees[r.code] ?? '');
        if (bps === null) {
            return { ok: false, message: `The restocking fee for ${r.label} must be a percent from 0 to 100, like 15 or 12.5` };
        }
        restocking_fees[r.code] = bps;
    }
    // The tint charge rule: the flat figure per container and the tiers that
    // may cover it. The rule follows the tint, not only the container.
    const flat = d.tintFlat.trim() === '' ? 0 : dollarsToCents(d.tintFlat.trim());
    if (isNaN(flat) || flat < 0) {
        return { ok: false, message: 'The tint charge must be a dollar amount, 0 or more' };
    }
    const tiers = [];
    for (const t of d.tintTiers) {
        const floor = t.minOunces.trim();
        if (floor !== '' && !/^\d+(\.\d{1,4})?$/.test(floor)) {
            return { ok: false, message: "A tint tier's colorant floor must be a decimal number of ounces, like 2 or 1.5" };
        }
        const cents = t.charge.trim() === '' ? 0 : dollarsToCents(t.charge.trim());
        if (isNaN(cents) || cents < 0) {
            return { ok: false, message: "A tint tier's charge must be a dollar amount, 0 or more" };
        }
        tiers.push({ base_tier: t.baseTier, min_colorant_oz: floor, charge_cents: cents });
    }
    return {
        ok: true,
        rules: {
            check_limit_cents,
            check_id_required: { mode: d.idMode, over_cents },
            restocking_fees,
            tint_charge: { flat_cents: flat, tiers },
            tinted_no_return: d.tintedNoReturn,
        },
    };
}

/**
 * The dealer's counter rules (CP-04, CP-06): the database wide check limit,
 * the check ID rule, and the restocking fee each return reason withholds.
 * Owner and admin only (the write is theirs on the server).
 */
@customElement('gable-counter-rules')
export class GableCounterRules extends LitElement {
    createRenderRoot() { return this; }

    @state() private _settings: PosSettings | null = null;
    @state() private _draft: Draft | null = null;
    @state() private _reasons: ReturnReason[] = [];
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;
    @state() private _fieldErrors: Record<string, string> = {};
    @state() private _success: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private _adopt(s: PosSettings, reasons: ReturnReason[]) {
        this._settings = s;
        this._reasons = reasons;
        const charge = s.rules.tint_charge ?? { flat_cents: 0, tiers: [] };
        this._draft = {
            limit: dollarsText(s.rules.check_limit_cents),
            idMode: s.rules.check_id_required.mode,
            idOver: dollarsText(s.rules.check_id_required.over_cents),
            fees: Object.fromEntries(reasons.map((r) => [r.code, bpsToPercent(s.rules.restocking_fees?.[r.code] ?? 0)])),
            tintFlat: dollarsText(charge.flat_cents),
            tintTiers: (charge.tiers ?? []).map((t) => ({
                baseTier: t.base_tier,
                minOunces: t.min_colorant_oz,
                charge: dollarsText(t.charge_cents),
            })),
            tintedNoReturn: s.rules.tinted_no_return ?? false,
        };
    }

    private async _load() {
        this._loading = true;
        try {
            const [settings, reasons] = await Promise.all([posService.settings(), posService.returnReasons()]);
            this._adopt(settings, reasons ?? []);
        } catch (err) {
            this._error = apiErrorMessage(err, 'Could not read the counter rules');
        } finally {
            this._loading = false;
        }
    }

    private _useSuggestedFees() {
        const d = this._draft;
        if (d === null || this._settings === null) return;
        const defaults = this._settings.rules.restocking_fee_defaults ?? {};
        this._draft = { ...d, fees: Object.fromEntries(this._reasons.map((r) => [r.code, bpsToPercent(defaults[r.code] ?? 0)])) };
    }

    /** Adds an empty tier to the draft. */
    private _addTier() {
        const d = this._draft;
        if (d === null) return;
        this._draft = { ...d, tintTiers: [...d.tintTiers, { baseTier: '', minOunces: '', charge: '' }] };
    }

    /** Removes the tier at the index. */
    private _removeTier(i: number) {
        const d = this._draft;
        if (d === null) return;
        this._draft = { ...d, tintTiers: d.tintTiers.filter((_, j) => j !== i) };
    }

    /** Changes one field of the tier at the index. */
    private _setTier(i: number, patch: Partial<DraftTier>) {
        const d = this._draft;
        if (d === null) return;
        this._draft = { ...d, tintTiers: d.tintTiers.map((t, j) => (j === i ? { ...t, ...patch } : t)) };
    }

    private async _save() {
        const d = this._draft;
        if (d === null || this._settings === null || this._saving) return;
        this._error = null;
        this._success = null;
        this._fieldErrors = {};
        const rules = draftRules(d, this._reasons);
        if (!rules.ok) {
            this._error = rules.message;
            return;
        }
        this._saving = true;
        try {
            this._adopt(await posService.saveSettings(rules.rules, this._settings.revision), this._reasons);
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
            { value: 'over_amount', label: 'Checks over an amount in total need the customer\'s ID' },
        ];
        return html`
            <div class="space-y-6">
                <div>
                    <h1 class="text-3xl font-bold text-white flex items-center gap-2">
                        ${icon(ShieldCheck, 24, 'w-6 h-6 text-gable-green')}
                        Counter rules
                    </h1>
                    <p class="text-slate-400 text-sm mt-1">
                        The rules every register of the dealer holds a check to, and the restocking fee a return withholds
                        for each reason. A customer's own check limit wins over the database wide one. Only the kind of ID
                        is ever kept, never its number.
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
                                    <span class="block text-xs uppercase tracking-wider text-slate-400">Checks over (dollars)</span>
                                    <input type="text" inputmode="decimal" name="id-over" data-testid="counter-rules-id-over"
                                        placeholder="Amount the rule starts at" .value=${d.idOver}
                                        @input=${(e: Event) => { this._draft = { ...d, idOver: (e.target as HTMLInputElement).value }; }}
                                        class="${FIELD_CLASS} font-mono max-w-48" />
                                    ${this._fieldErrors['rules.check_id_required.over_cents']
                                        ? html`<span class="text-xs text-red-400">${this._fieldErrors['rules.check_id_required.over_cents']}</span>` : nothing}
                                </label>
                            </div>` : nothing}
                    </fieldset>

                    <fieldset class="space-y-3" data-testid="counter-rules-fees">
                        <legend class="text-xs uppercase tracking-wider text-slate-400">Restocking fees</legend>
                        <p class="text-xs text-slate-500">
                            What a return withholds from the refund of the lines that carry the reason, taken to the cent and never more
                            than the line. Zero keeps the whole refund. The reasons that are the customer's own doing are the ones a
                            fee belongs on; a dealer's own mistake carries none.
                        </p>
                        ${this._reasons.map((r) => {
                            const suggested = this._settings?.rules.restocking_fee_defaults?.[r.code] ?? 0;
                            return html`
                            <label class="block space-y-1">
                                <span class="flex items-baseline justify-between gap-2 text-sm text-slate-200">
                                    <span>${r.label}</span>
                                    ${suggested > 0 ? html`<span class="text-xs text-slate-500" data-testid="counter-rules-fee-suggested-${r.code}">Suggested ${bpsToPercent(suggested)}%</span>` : nothing}
                                </span>
                                <div class="flex items-center gap-2">
                                    <input type="text" inputmode="decimal" name="fee-${r.code}" data-testid="counter-rules-fee-${r.code}"
                                        placeholder="0" .value=${this._draft?.fees[r.code] ?? ''}
                                        @input=${(e: Event) => { if (this._draft) { this._draft = { ...this._draft, fees: { ...this._draft.fees, [r.code]: (e.target as HTMLInputElement).value } }; } }}
                                        class="${FIELD_CLASS} font-mono max-w-28" />
                                    <span class="text-xs text-slate-500">percent</span>
                                </div>
                            </label>`;
                        })}
                        <button type="button" data-testid="counter-rules-fees-suggested" @click=${() => this._useSuggestedFees()}
                            class="inline-flex items-center rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 min-h-11">
                            Use the suggested fees
                        </button>
                    </fieldset>

                    <fieldset class="space-y-3" data-testid="counter-rules-tint">
                        <legend class="text-xs uppercase tracking-wider text-slate-400">Tint charge (row CP-16)</legend>
                        <p class="text-xs text-slate-500">
                            What the counter charges for a tinted can. The rule follows the tint, not only the container: a tier
                            covers the base tier and the colorant floor it names, and the most specific tier that covers the mix
                            wins. The flat figure covers every mix no tier covers; zero folds the tint into the shelf price.
                        </p>
                        <label class="block space-y-1">
                            <span class="text-xs uppercase tracking-wider text-slate-400">Charge per container (dollars)</span>
                            <input type="text" inputmode="decimal" name="tint-flat" data-testid="counter-rules-tint-flat"
                                placeholder="0" .value=${d.tintFlat}
                                @input=${(e: Event) => { this._draft = { ...d, tintFlat: (e.target as HTMLInputElement).value }; }}
                                class="${FIELD_CLASS} font-mono max-w-28" />
                            ${this._fieldErrors['rules.tint_charge.flat_cents']
                                ? html`<span class="text-xs text-red-400">${this._fieldErrors['rules.tint_charge.flat_cents']}</span>` : nothing}
                        </label>
                        ${d.tintTiers.map((t, i) => html`
                            <div class="flex items-end gap-2 flex-wrap" data-testid="counter-rules-tint-tier-${i}">
                                <label class="block space-y-1">
                                    <span class="block text-xs uppercase tracking-wider text-slate-400">Base tier</span>
                                    <gable-select label="Base tier"
                                        .value=${t.baseTier}
                                        .options=${[
                                            { value: '', label: 'Any base' },
                                            { value: 'flat', label: 'Flat base' },
                                            { value: 'deep', label: 'Deep base' },
                                        ]}
                                        @change=${(e: Event) => { this._setTier(i, { baseTier: (e.target as HTMLSelectElement).value as DraftTier['baseTier'] }); }}>
                                    </gable-select>
                                </label>
                                <label class="block space-y-1">
                                    <span class="block text-xs uppercase tracking-wider text-slate-400">Colorant floor (oz)</span>
                                    <input type="text" inputmode="decimal" data-testid="counter-rules-tint-floor-${i}"
                                        placeholder="Any amount" .value=${t.minOunces}
                                        @input=${(e: Event) => { this._setTier(i, { minOunces: (e.target as HTMLInputElement).value }); }}
                                        class="${FIELD_CLASS} font-mono max-w-28" />
                                </label>
                                <label class="block space-y-1">
                                    <span class="block text-xs uppercase tracking-wider text-slate-400">Charge (dollars)</span>
                                    <input type="text" inputmode="decimal" data-testid="counter-rules-tint-charge-${i}"
                                        placeholder="0" .value=${t.charge}
                                        @input=${(e: Event) => { this._setTier(i, { charge: (e.target as HTMLInputElement).value }); }}
                                        class="${FIELD_CLASS} font-mono max-w-28" />
                                </label>
                                <button type="button" data-testid="counter-rules-tint-tier-remove-${i}"
                                    @click=${() => this._removeTier(i)}
                                    class="inline-flex items-center rounded border border-white/20 px-3 py-2 text-xs font-semibold text-slate-200 hover:bg-white/5 min-h-11">
                                    Remove the tier
                                </button>
                            </div>`)}
                        <button type="button" data-testid="counter-rules-tint-add-tier" @click=${() => this._addTier()}
                            class="inline-flex items-center rounded border border-white/20 px-3 py-1.5 text-xs font-semibold text-slate-200 hover:bg-white/5 min-h-11">
                            Add a tier
                        </button>
                        <label class="flex items-center gap-2 text-sm text-slate-200" data-testid="counter-rules-tint-no-return">
                            <input type="checkbox" name="tinted-no-return" .checked=${d.tintedNoReturn}
                                @change=${(e: Event) => { this._draft = { ...d, tintedNoReturn: (e.target as HTMLInputElement).checked }; }} />
                            A tinted can is not taken back
                        </label>
                        <span class="block text-xs text-slate-500">
                            The trade rule for a can the store mixed to order: it is re-tinted and put on the marked down rack,
                            never back on the shelf. A mis tint (the store mixed the wrong colour) is always returned.
                        </span>
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
