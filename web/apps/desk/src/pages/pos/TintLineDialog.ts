// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select';
import { posService } from '../../services/POSService';
import {
    tintService,
    type TintSystem,
    type TintBase,
    type TintColor,
    type TintFormula,
    type TintSavedColor,
    type TintRescaledComponent,
} from '../../services/TintService';
import type { Sale, TintAmount } from '../../types/pos';
import { formatCents, formatPrice2 } from '../../lib/utils.ts';
import { formatAmount, fineUnits, colorantUnits, priceWithTintCharge, tintPayload } from './tint.ts';

const FIELD = 'width:100%;box-sizing:border-box;padding:10px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:15px;outline:none';
const LABEL = 'display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin:12px 0 6px';

/** One colorant of the dialog's working state: its identity and its editable amount. */
interface WorkingComponent {
    colorant_code: string;
    colorant_name: string;
    amount: TintAmount;
}

/**
 * The counter's tint line (row CP-16): pick the base paint, the colour and
 * the container size; the formula comes from the catalogue or is rescaled to
 * the chosen size with the rounding flagged; every colorant amount stays
 * editable, because a formula that is off by a shot has to be fixable at the
 * counter. The dialog writes a normal sale line with a tint attached. It
 * also offers the customer's saved colours and saves the chosen colour back
 * for the next order.
 */
@customElement('gable-tint-line-dialog')
export class TintLineDialog extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: 'sale-id' }) saleId = '';
    @property({ attribute: 'line-id' }) lineId = '';
    @property({ attribute: 'product-id' }) productId = '';
    @property() quantity = '1';
    @property() uom = '';
    /** The line's own unit price in ten thousandths, before the tint charge. */
    @property({ type: Number, attribute: 'base-price' }) basePrice = 0;
    @property({ attribute: 'customer-id' }) customerId = '';

    @state() private systems: TintSystem[] = [];
    @state() private bases: TintBase[] = [];
    @state() private baseId = '';
    @state() private colorQuery = '';
    @state() private colors: TintColor[] = [];
    @state() private colorId = '';
    @state() private formulas: TintFormula[] = [];
    @state() private sizeCode = '';
    @state() private components: WorkingComponent[] = [];
    @state() private rescaled = false;
    @state() private chargeCents = 0;
    @state() private savedColors: TintSavedColor[] = [];
    @state() private saveName = '';
    @state() private saveJob = '';
    @state() private failure = '';
    @state() private busy = false;
    private loaded = false;

    async connectedCallback() {
        super.connectedCallback();
        if (this.loaded) return;
        this.loaded = true;
        await this.loadCatalogue();
    }

    private async loadCatalogue() {
        try {
            const [systems, bases] = await Promise.all([
                tintService.systems(),
                tintService.bases({ product_id: this.productId }),
            ]);
            this.systems = systems.items;
            this.bases = bases.items;
            if (this.bases.length > 0) await this.pickBase(this.bases[0].id);
            if (this.customerId) {
                const saved = await tintService.savedColors(this.customerId);
                this.savedColors = saved.items;
            }
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to read the tint catalogue';
        }
    }

    /**
     * Asks the server what the dealer's rule charges for this tint. The rule
     * follows the tint, not only the container, so the charge is read with the
     * tint's own numbers: the colorant total in the system's whole units and
     * the base's tier. The server owns the rule, so the screen never computes
     * the charge itself.
     */
    private async refreshCharge() {
        const base = this.bases.find((b) => b.id === this.baseId);
        const sys = this.systems.find((s) => s.id === base?.tint_system_id);
        const { units, denominator } = colorantUnits(this.components, sys?.unit_denominator);
        try {
            const charge = await tintService.charge({
                colorant_units: units,
                unit_denominator: denominator,
                base_tier: base?.is_deepest ? 'deep' : 'flat',
            });
            this.chargeCents = charge.tint_charge_cents ?? 0;
            this.failure = '';
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to read the tint charge';
        }
    }

    private colorOf(id: string): TintColor | undefined {
        return this.colors.find((c) => c.id === id) ?? undefined;
    }

    /** The name of the formula book the chosen base belongs to, in the counter's own words. */
    private get bookName(): string {
        const base = this.bases.find((b) => b.id === this.baseId);
        const sys = this.systems.find((s) => s.id === base?.tint_system_id);
        if (!sys) return '';
        return sys.manufacturer ? `${sys.manufacturer} ${sys.name}` : sys.name;
    }

    private async pickBase(baseId: string) {
        this.baseId = baseId;
        this.colorId = '';
        this.formulas = [];
        this.components = [];
        await this.searchColors('');
        await this.refreshCharge();
    }

    private async searchColors(q: string) {
        this.colorQuery = q;
        try {
            const base = this.bases.find((b) => b.id === this.baseId);
            const systemID = base?.tint_system_id ?? '';
            const found = await tintService.colors({ tint_system_id: systemID, q });
            this.colors = found.items;
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to read the colours';
        }
    }

    /** Loads the formulas of the colour in the chosen base and takes the first size. */
    private async pickColor(colorId: string) {
        this.colorId = colorId;
        try {
            const found = await tintService.formulas({ color_id: colorId, base_id: this.baseId });
            this.formulas = found.items;
            if (this.formulas.length > 0) await this.pickSize(this.formulas[0].container_size_code);
            else this.components = [];
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to read the formulas';
        }
    }

    /** Chooses a size: the book's own row when it has one, otherwise a rescale from the first. */
    private async pickSize(sizeCode: string) {
        this.sizeCode = sizeCode;
        const exact = this.formulas.find((f) => f.container_size_code === sizeCode);
        const source = exact ?? this.formulas[0];
        if (!source) { this.components = []; await this.refreshCharge(); return; }
        if (exact) {
            this.components = source.components.map((c) => ({ colorant_code: c.colorant_code, colorant_name: c.colorant_name, amount: { ...c.amount } }));
            this.rescaled = false;
            await this.refreshCharge();
            return;
        }
        // a rescale: the counter sees the rounding flag and may override any amount
        const target = this.sizeVolumes().find((s) => s.code === sizeCode);
        const res = await tintService.rescale(source.id, target?.oz ?? source.container_volume_oz);
        this.components = res.components.map((c: TintRescaledComponent) => ({ colorant_code: c.colorant_code, colorant_name: c.colorant_name, amount: { ...c.amount } }));
        this.rescaled = true;
        await this.refreshCharge();
    }

    /** The size choices: every size the catalogue holds for the colour, plus the quart for a gallon only book. */
    private sizeVolumes(): Array<{ code: string; oz: number }> {
        const sizes: Array<{ code: string; oz: number }> = this.formulas.map((f) => ({ code: f.container_size_code, oz: f.container_volume_oz }));
        if (sizes.length > 0 && !sizes.some((s) => s.code === 'QT')) {
            sizes.push({ code: 'QT', oz: Math.round(sizes[0].oz / 4) });
        }
        return sizes;
    }

    private setAmount(i: number, patch: Partial<TintAmount>) {
        this.components = this.components.map((c, j) => (j === i ? { ...c, amount: { ...c.amount, ...patch } } : c));
        // the charge follows the mix: an amount the counter corrects moves it
        void this.refreshCharge();
    }

    /** A saved colour fills the whole dialog: the base, the colour and its first formula. */
    private async useSavedColor(saved: TintSavedColor) {
        this.saveName = saved.name;
        this.saveJob = saved.job_note;
        const color = this.colors.find((c) => c.id === saved.color_id);
        if (color) {
            await this.pickColor(saved.color_id);
        } else {
            // the saved colour sits outside the current list: load it through its formulas
            try {
                const found = await tintService.formulas({ color_id: saved.color_id, base_id: this.baseId });
                this.colorId = saved.color_id;
                this.formulas = found.items;
                if (this.formulas.length > 0) await this.pickSize(this.formulas[0].container_size_code);
            } catch (err: unknown) {
                this.failure = err instanceof Error ? err.message : 'Failed to read the saved colour';
            }
        }
    }

    private async add() {
        const color = this.colorOf(this.colorId);
        if (!color || this.components.length === 0) {
            this.failure = 'Choose a base, a colour and a size first';
            return;
        }
        try {
            this.busy = true;
            const sale = await posService.addItemTint(
                this.saleId,
                this.productId,
                {
                    formula_id: (this.formulas.find((f) => f.container_size_code === this.sizeCode) ?? this.formulas[0])?.id ?? null,
                    color_code: color.manufacturer_code ?? '',
                    color_name: color.name,
                    base_code: this.bases.find((b) => b.id === this.baseId)?.code ?? '',
                    container_size_code: this.sizeCode,
                    rescaled: this.rescaled,
                    components: this.components.map((c) => ({ colorant_code: c.colorant_code, colorant_name: c.colorant_name, amount: c.amount })),
                },
                this.quantity || '1',
                this.uom || undefined,
                this.lineId || undefined,
            );
            // the save is offered on every tinted sale: it is what makes a repeat order the same formula
            if (this.customerId && this.saveName.trim()) {
                try {
                    await tintService.saveColor({
                        customer_id: this.customerId, color_id: this.colorId,
                        name: this.saveName.trim(), job_note: this.saveJob.trim(),
                    });
                } catch {
                    // the line is added; a failed save must not lose the sale
                }
            }
            this.dispatchEvent(new CustomEvent<Sale>('tint-added', { detail: sale, bubbles: true }));
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to add the tinted line';
        } finally {
            this.busy = false;
        }
    }

    private cancel() {
        this.dispatchEvent(new CustomEvent('tint-cancel', { bubbles: true }));
    }

    render() {
        if (this.bases.length === 0 && !this.failure) {
            return html`
                <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Tint paint</h2>
                <p style="margin:0 0 14px;font-size:13px;color:rgb(var(--gb-till-muted))">This product is not linked to a tint base paint. Link a base to the product in the tint catalogue first.</p>
                <button type="button" data-testid="tint-cancel" @click=${() => this.cancel()} style="${FIELD};cursor:pointer">Close</button>
                ${this.failure ? this.failureLine() : nothing}
            `;
        }
        const color = this.colorOf(this.colorId);
        const price = priceWithTintCharge(this.basePrice, this.chargeCents);
        return html`
            <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Tint paint</h2>
            <p style="margin:0 0 14px;font-size:13px;color:rgb(var(--gb-till-muted))">Pick the base, the colour and the size. The formula shows below and every amount can be corrected at the counter.</p>
            ${this.bookName ? html`<p style="margin:-8px 0 12px;font-size:12px;color:rgb(var(--gb-till-soft))" data-testid="tint-book">Formula book: ${this.bookName}</p>` : nothing}

            <label style=${LABEL} for="tint-base">Base paint</label>
            <gable-select id="tint-base" data-testid="tint-base" label="Base paint" .value=${this.baseId}
                .options=${this.bases.map((b) => ({ value: b.id, label: `${b.code} ${b.name}` }))}
                @change=${(e: Event) => this.pickBase((e.target as HTMLSelectElement).value)}>
            </gable-select>

            <label style=${LABEL} for="tint-color-search">Colour</label>
            <input id="tint-color-search" type="text" data-testid="tint-color-search" style=${FIELD} placeholder="Search a colour by name or code"
                .value=${this.colorQuery} @input=${(e: Event) => this.searchColors((e.target as HTMLInputElement).value)} />
            <div data-testid="tint-color-results" role="listbox" style="margin-top:6px;max-height:140px;overflow-y:auto">
                ${this.colors.map((c) => html`
                    <button type="button" role="option" aria-selected=${c.id === this.colorId ? 'true' : 'false'}
                        data-testid="tint-color-option" data-code=${c.manufacturer_code ?? ''}
                        @click=${() => this.pickColor(c.id)}
                        style="display:block;width:100%;text-align:left;padding:8px 10px;margin-bottom:4px;border-radius:8px;cursor:pointer;font-size:14px;border:1px solid ${c.id === this.colorId ? 'rgb(var(--gb-till-accent))' : 'rgb(var(--gb-till-edge))'};background:${c.id === this.colorId ? 'rgb(var(--gb-till-field))' : 'transparent'};color:rgb(var(--gb-till-text))">
                        ${c.name}${c.manufacturer_code ? ` (${c.manufacturer_code})` : ''}${c.is_custom ? ' - store match' : ''}
                    </button>
                `)}
            </div>

            ${this.formulas.length > 0 || this.sizeCode ? html`
                <label style=${LABEL} for="tint-size">Container size</label>
                <gable-select id="tint-size" data-testid="tint-size" label="Container size" .value=${this.sizeCode}
                    .options=${this.sizeVolumes().map((s) => ({ value: s.code, label: s.code }))}
                    @change=${(e: Event) => this.pickSize((e.target as HTMLSelectElement).value)}>
                </gable-select>
            ` : nothing}

            ${this.components.length > 0 ? html`
                <p style="margin:14px 0 6px;font-size:12px;color:rgb(var(--gb-till-muted))">
                    Formula${this.rescaled ? html` <strong data-testid="tint-rescaled" style="color:rgb(var(--gb-till-warn))">rescaled to this size, check the amounts</strong>` : nothing}
                </p>
                <div data-testid="tint-components">
                    ${this.components.map((c, i) => html`
                        <div style="display:flex;gap:6px;align-items:center;margin-bottom:6px">
                            <span style="flex:1;font-size:14px">${c.colorant_code} ${c.colorant_name}</span>
                            <input type="number" min="0" data-testid="tint-whole-${i}" aria-label="Whole ounces of ${c.colorant_code}" style="width:64px;${FIELD}" .value=${String(c.amount.whole_oz)}
                                @input=${(e: Event) => this.setAmount(i, { whole_oz: Math.max(0, Number((e.target as HTMLInputElement).value) || 0) })} />
                            <input type="number" min="0" data-testid="tint-coarse-${i}" aria-label="1/32 counts of ${c.colorant_code}" style="width:64px;${FIELD}" .value=${String(c.amount.increments_coarse)}
                                @input=${(e: Event) => this.setAmount(i, { increments_coarse: Math.max(0, Number((e.target as HTMLInputElement).value) || 0) })} />
                            <input type="number" min="0" data-testid="tint-fine-${i}" aria-label="1/${c.amount.unit_denominator} counts of ${c.colorant_code}" style="width:72px;${FIELD}" .value=${String(c.amount.increments_fine)}
                                @input=${(e: Event) => this.setAmount(i, { increments_fine: Math.max(0, Number((e.target as HTMLInputElement).value) || 0) })} />
                            <span style="font-size:12px;color:rgb(var(--gb-till-muted));min-width:80px">${formatAmount(c.amount)}</span>
                        </div>
                    `)}
                </div>
                <p style="margin:10px 0 0;font-size:13px" data-testid="tint-price">
                    Line price ${formatPrice2(price)}${this.chargeCents > 0 ? ` (includes the ${formatCents(this.chargeCents)} tint charge)` : ''}
                </p>
            ` : nothing}

            ${this.savedColors.length > 0 ? html`
                <span style=${LABEL}>Saved colours for this customer</span>
                <div data-testid="tint-saved-colors">
                    ${this.savedColors.map((s) => html`
                        <button type="button" @click=${() => this.useSavedColor(s)}
                            style="display:block;width:100%;text-align:left;padding:8px 10px;margin-bottom:4px;border-radius:8px;cursor:pointer;font-size:13px;border:1px solid rgb(var(--gb-till-edge));background:transparent;color:rgb(var(--gb-till-text))">
                            ${s.name}${s.job_note ? ` - ${s.job_note}` : ''}
                        </button>
                    `)}
                </div>
            ` : nothing}

            ${this.customerId ? html`
                <label style=${LABEL} for="tint-save-name">Save this colour for the customer as (optional)</label>
                <input id="tint-save-name" type="text" data-testid="tint-save-name" style=${FIELD} .value=${this.saveName}
                    @input=${(e: Event) => { this.saveName = (e.target as HTMLInputElement).value; }} placeholder="For example Living room" />
                <label style=${LABEL} for="tint-save-job">Job or room note (optional)</label>
                <input id="tint-save-job" type="text" data-testid="tint-save-job" style=${FIELD} .value=${this.saveJob}
                    @input=${(e: Event) => { this.saveJob = (e.target as HTMLInputElement).value; }} placeholder="For example jobsite" />
            ` : nothing}

            ${this.failure ? this.failureLine() : nothing}
            <div style="display:flex;gap:8px;margin-top:20px">
                <button type="button" data-testid="tint-cancel" @click=${() => this.cancel()}
                    style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
                <button type="button" data-testid="tint-add" @click=${() => this.add()} ?disabled=${this.busy || !color || this.components.length === 0}
                    style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-deep-space));font-size:15px;font-weight:700;cursor:pointer">${this.busy ? 'Adding\u2026' : `Add tinted line ${formatPrice2(price)}`}</button>
            </div>
        `;
    }

    private failureLine() {
        return html`<p role="alert" data-testid="tint-failure" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.failure}</p>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-tint-line-dialog': TintLineDialog;
    }
}

export { fineUnits, tintPayload };
