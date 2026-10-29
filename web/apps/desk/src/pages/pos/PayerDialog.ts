// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// <gable-payer-dialog> the counter's payer capture for a large cash crossing
// (CP-17a). The rule the branch's country picks names the fields; the values
// the rule needs are captured once, before Pay. The sensitive values (the ID
// number and the taxpayer identification number) are masked after they are
// captured and never shown in cleartext again on this screen. A Canadian
// crossing carries the plain sentence that the capture is for the dealer's
// own records and is not a FINTRAC report. The branch's walk-in identity
// floor asks the same way, with one difference: a walk-in who declines to
// identify lets the sale go ahead, and the fact is recorded for a manager.
import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select.ts';

/** What the preview said the rule needs. */
export interface PayerAsk {
    /** The field names the rule needs, from the preview's fields list. */
    fields: string[];
    /** True for the Canadian dealer threshold: the note shows and no taxpayer id is asked. */
    dealerThreshold: boolean;
    /**
     * True when the branch's walk-in identity floor is the only question the
     * counter asks: who paid, so their cash sales are counted together. A sale
     * that also crosses the rule's threshold asks for the payer's details and
     * no decline answers it, so the counter says the crossing's words.
     */
    floor?: boolean;
    /** True when the rule's threshold is crossed: the payer's details are required and a decline does not answer it. */
    thresholdCrossed?: boolean;
}

/** The captured payer, in the wire's shape. */
export interface PayerCapture {
    name: string;
    address: string;
    occupation: string;
    date_of_birth: string;
    id_type: string;
    id_number: string;
    taxpayer_id: string;
    third_party_payer: boolean;
}

const ID_TYPES = ['drivers_license', 'state_id', 'passport', 'military_id', 'other'];

const ID_TYPE_LABELS: Record<string, string> = {
    drivers_license: "Driver's license",
    state_id: 'State ID',
    passport: 'Passport',
    military_id: 'Military ID',
    other: 'Other',
};

const EMPTY_PAYER: PayerCapture = {
    name: '',
    address: '',
    occupation: '',
    date_of_birth: '',
    id_type: '',
    id_number: '',
    taxpayer_id: '',
    third_party_payer: false,
};

/** The last four of a sensitive value, the only part shown after capture. */
export function masked(value: string): string {
    const trimmed = value.trim();
    if (trimmed === '') return '';
    return `****${trimmed.slice(-4)}`;
}

@customElement('gable-payer-dialog')
export class GablePayerDialog extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: false }) ask: PayerAsk | null = null;

    /** Set when the completion was refused and the capture is asked again. */
    @property({ type: Boolean }) refused = false;

    /** The already captured payer, shown masked; a fresh capture starts empty. */
    @property({ attribute: false }) prior: PayerCapture | null = null;

    @state() private _draft: PayerCapture = { ...EMPTY_PAYER };

    private _update<K extends keyof PayerCapture>(key: K, value: PayerCapture[K]) {
        this._draft = { ...this._draft, [key]: value };
    }

    private _needs(field: string): boolean {
        return this.ask?.fields.includes(field) ?? false;
    }

    /** Save is live only when every field the rule needs is filled. */
    private get _complete(): boolean {
        const needs = this.ask?.fields ?? [];
        if (needs.includes('third_party_payer')) {
            // a checkbox is an answer either way
            const rest = needs.filter((f) => f !== 'third_party_payer');
            return rest.every((f) => this._draft[f as keyof PayerCapture] !== '' && this._draft[f as keyof PayerCapture] !== undefined);
        }
        return needs.every((f) => this._draft[f as keyof PayerCapture] !== '' && this._draft[f as keyof PayerCapture] !== undefined);
    }

    private _cancel() {
        this.dispatchEvent(new CustomEvent('payer-cancel', { bubbles: true, composed: true }));
    }

    private _save() {
        if (!this._complete) return;
        this.dispatchEvent(new CustomEvent<PayerCapture>('payer-save', {
            detail: { ...this._draft }, bubbles: true, composed: true,
        }));
    }

    /**
     * The walk-in declined to identify over the branch's floor. The sale goes
     * ahead and the fact is recorded for the manager's worklist, so the
     * counter says so plainly. A crossing is not answered this way: the
     * payer's details are the condition of the completion.
     */
    private _decline() {
        this.dispatchEvent(new CustomEvent('payer-decline', { bubbles: true, composed: true }));
    }

    /** The decline answers the floor's ask only; a crossing cannot be declined. */
    private get _canDecline(): boolean {
        return (this.ask?.floor ?? false) && !(this.ask?.thresholdCrossed ?? false);
    }

    private _field(label: string, key: keyof PayerCapture, placeholder: string, type = 'text') {
        return html`
            <label class="block text-xs uppercase tracking-wider text-zinc-500 font-semibold">
            ${label}
            <input
                type=${type}
                .value=${this._draft[key] as string}
                placeholder=${placeholder}
                @input=${(e: Event) => this._update(key, (e.target as HTMLInputElement).value as never)}
                class="mt-1 w-full bg-deep-space/60 border border-white/10 rounded-md px-3 py-2 text-sm text-white font-normal normal-case tracking-normal focus:outline-none focus:border-gable-green/50"
                data-testid=${`payer-${key}`}
            />
            </label>
        `;
    }

    render() {
        if (!this.ask) return nothing;
        return html`
            <div
                class="fixed inset-0 z-50 bg-deep-space/80 backdrop-blur-sm flex items-center justify-center p-4"
                @click=${() => this._cancel()}
                data-testid="payer-dialog"
            >
                <div
                    class="bg-slate-steel border border-white/10 rounded-xl w-full max-w-xl shadow-elevation-2 max-h-[90vh] overflow-y-auto"
                    @click=${(e: MouseEvent) => e.stopPropagation()}
                >
                    <div class="px-6 py-4 border-b border-white/5">
                        <h2 class="text-lg font-semibold text-white" data-testid="payer-title">${this.ask.floor
                            ? 'Who is paying for this walk-in?'
                            : "This payment needs the payer's details"}</h2>
                        <p class="text-sm text-zinc-400 mt-1">
                            ${this.ask.floor
                                ? html`This walk-in paid cash over the branch's identity floor. Ask who they are so their cash sales are counted together. If they decline, the sale still goes ahead and it is recorded for a manager to review.`
                                : this.ask.dealerThreshold
                                    ? html`A cash payment at or above the dealer's threshold. This is for the dealer's own records. It is not a FINTRAC report.`
                                    : 'A cash payment that takes the payer over the large cash threshold. The details are kept for the report the dealer files.'}
                        </p>
                    </div>
                    ${this.refused ? html`
                        <div class="mx-6 mt-4 bg-safety-red/10 border border-safety-red/30 text-safety-red text-sm px-4 py-3 rounded-lg" data-testid="payer-refused">
                            ${this.ask.floor
                                ? 'Ask who paid for this walk-in, or record that they declined to identify.'
                                : "The payer's details are required before this payment can complete."}
                        </div>
                    ` : nothing}
                    <div class="p-6 grid grid-cols-1 md:grid-cols-2 gap-4">
                        ${this._field('Name', 'name', 'Riverbend Builders')}
                        ${this._field('Date of birth', 'date_of_birth', 'YYYY-MM-DD')}
                        <div class="md:col-span-2">${this._field('Address', 'address', '12 Mill Road, Kelowna')}</div>
                        ${this._field('Occupation', 'occupation', 'Contractor')}
                        <label class="block text-xs uppercase tracking-wider text-zinc-500 font-semibold">
                            ID type
                            <gable-select
                                class="mt-1 block font-normal normal-case tracking-normal"
                                .value=${this._draft.id_type}
                                .options=${[{ value: '', label: 'Choose the kind' }, ...ID_TYPES.map((t) => ({ value: t, label: ID_TYPE_LABELS[t] }))]}
                                @change=${(e: Event) => this._update('id_type', (e.target as HTMLInputElement).value as never)}
                                data-testid="payer-id_type"
                            ></gable-select>
                        </label>
                        ${this._needs('id_number') ? this._field('ID number', 'id_number', 'Shown masked once captured') : nothing}
                        ${this._needs('taxpayer_id') ? this._field('Taxpayer identification number', 'taxpayer_id', 'Shown masked once captured') : nothing}
                        ${this._needs('third_party_payer') ? html`
                            <label class="flex items-center gap-2 text-sm text-zinc-300 md:col-span-2">
                                <input
                                    type="checkbox"
                                    .checked=${this._draft.third_party_payer}
                                    @change=${(e: Event) => this._update('third_party_payer', (e.target as HTMLInputElement).checked)}
                                    class="rounded bg-slate-steel border-white/10 text-gable-green focus:ring-gable-green/30"
                                    data-testid="payer-third_party_payer"
                                />
                                Someone else is paying on the payer's behalf
                            </label>
                        ` : nothing}
                    </div>
                    ${this.prior ? html`
                        <div class="px-6 pb-2 text-xs text-zinc-500">
                            Captured: ${this.prior.name}, ID ${masked(this.prior.id_number)}${this.prior.taxpayer_id ? `, taxpayer ID ${masked(this.prior.taxpayer_id)}` : nothing}
                        </div>
                    ` : nothing}
                    <div class="px-6 py-4 border-t border-white/5 flex items-center justify-end gap-3">
                        ${this._canDecline ? html`
                            <button
                                @click=${() => this._decline()}
                                class="px-4 py-2 text-sm text-zinc-300 hover:bg-white/5 rounded-lg transition-colors"
                                data-testid="payer-decline"
                            >They declined to identify</button>
                        ` : nothing}
                        <button
                            @click=${() => this._cancel()}
                            class="px-4 py-2 text-sm text-zinc-300 hover:bg-white/5 rounded-lg transition-colors"
                            data-testid="payer-cancel"
                        >Cancel</button>
                        <button
                            @click=${() => this._save()}
                            ?disabled=${!this._complete}
                            class="px-4 py-2 text-sm font-semibold bg-gable-green text-deep-space rounded-lg hover:bg-gable-green/90 transition-colors disabled:opacity-50"
                            data-testid="payer-save"
                        >Capture the payer</button>
                    </div>
                </div>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-payer-dialog': GablePayerDialog;
    }
}
