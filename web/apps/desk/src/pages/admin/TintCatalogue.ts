// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { Palette } from 'lucide';
import { apiErrorMessage } from '../../services/apiError';
import { posService } from '../../services/POSService';
import { tintService } from '../../services/TintService';
import type { TintBase, TintColor, TintColorant, TintSystem } from '../../services/TintService';
import '../../components/ui/gable-select.ts';
import type { QuickSearchResult } from '../../types/pos.ts';

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green';
const BUTTON_CLASS =
    'px-3 py-2 rounded bg-gable-green text-deep-space text-sm font-semibold disabled:opacity-50';

/** One colorant row of the formula being entered, in the guide's own amount columns. */
interface DraftComponent {
    colorantId: string;
    whole: string;
    coarse: string;
    fine: string;
}

/**
 * The tint catalogue (CP-16, items 341 and 366): the dealer's tint systems,
 * their bases and the link of each base to the stocked base paint product the
 * counter sells, the colorants, the colours and one formula at a time.
 * Admin and owner only (the writes are theirs on the server).
 */
@customElement('gable-tint-catalogue')
export class GableTintCatalogue extends LitElement {
    createRenderRoot() { return this; }

    @state() private _systems: TintSystem[] = [];
    @state() private _systemId = '';
    @state() private _bases: TintBase[] = [];
    @state() private _colorants: TintColorant[] = [];
    @state() private _colors: TintColor[] = [];
    @state() private _loading = true;
    @state() private _busy = false;
    /** The message of the last action, by the card it belongs to: it shows beside that action. */
    @state() private _notes: Record<string, { kind: 'error' | 'ok'; text: string }> = {};
    /** The product search per base id: the text typed and what it found. */
    @state() private _search: Record<string, { text: string; found: QuickSearchResult[] }> = {};
    /** The product each linked base shows, by product id, once picked here. */
    @state() private _labels: Record<string, string> = {};
    @state() private _newSystem = { code: '', name: '', manufacturer: '', denominator: '384' };
    @state() private _newBase = { code: '', name: '', fill: '122', deepest: false };
    @state() private _newColorant = { code: '', name: '' };
    @state() private _newColor = { code: '', name: '' };
    @state() private _formula = { colorId: '', baseId: '', size: 'GAL', volume: '128', components: [] as DraftComponent[] };

    connectedCallback() {
        super.connectedCallback();
        void this._loadSystems();
    }

    private get _system(): TintSystem | undefined {
        return this._systems.find((s) => s.id === this._systemId);
    }

    private _note(card: string, kind: 'error' | 'ok', text: string) {
        this._notes = { [card]: { kind, text } };
    }

    /** Runs one write; its outcome is shown in the card that asked for it. */
    private async _run(card: string, done: string, work: () => Promise<void>) {
        if (this._busy) return;
        this._busy = true;
        this._notes = {};
        try {
            await work();
            this._note(card, 'ok', done);
        } catch (err) {
            this._note(card, 'error', apiErrorMessage(err, 'Could not save the change'));
        } finally {
            this._busy = false;
        }
    }

    private async _loadSystems() {
        this._loading = true;
        try {
            this._systems = (await tintService.systems()).items;
            if (this._systemId === '' && this._systems.length > 0) this._systemId = this._systems[0].id;
            await this._loadSystem();
        } catch (err) {
            this._note('page', 'error', apiErrorMessage(err, 'Could not read the tint catalogue'));
        } finally {
            this._loading = false;
        }
    }

    private async _loadSystem() {
        if (this._systemId === '') {
            this._bases = [];
            this._colorants = [];
            this._colors = [];
            return;
        }
        const [bases, colorants, colors] = await Promise.all([
            tintService.bases({ tint_system_id: this._systemId }),
            tintService.colorants(this._systemId),
            tintService.colors({ tint_system_id: this._systemId }),
        ]);
        this._bases = bases.items;
        this._colorants = colorants.items;
        this._colors = colors.items;
    }

    private async _pickSystem(id: string) {
        this._systemId = id;
        this._formula = { ...this._formula, colorId: '', baseId: '', components: [] };
        try {
            await this._loadSystem();
        } catch (err) {
            this._note('page', 'error', apiErrorMessage(err, 'Could not read the tint system'));
        }
    }

    private async _addSystem() {
        const n = this._newSystem;
        const denominator = Number(n.denominator);
        await this._run('system', 'Tint system added', async () => {
            const made = await tintService.createSystem({
                code: n.code.trim(), name: n.name.trim(), manufacturer: n.manufacturer.trim(), unit_denominator: denominator,
            });
            this._newSystem = { code: '', name: '', manufacturer: '', denominator: n.denominator };
            this._systems = [...this._systems, made];
            await this._pickSystem(made.id);
        });
    }

    private async _addBase() {
        const n = this._newBase;
        await this._run('bases', 'Base added', async () => {
            await tintService.createBase({
                tint_system_id: this._systemId, code: n.code.trim(), name: n.name.trim(),
                fill_volume_oz: Number(n.fill), is_deepest: n.deepest,
            });
            this._newBase = { code: '', name: '', fill: n.fill, deepest: false };
            await this._loadSystem();
        });
    }

    private async _addColorant() {
        const n = this._newColorant;
        await this._run('colorants', 'Colorant added', async () => {
            await tintService.createColorant({ tint_system_id: this._systemId, code: n.code.trim(), name: n.name.trim() });
            this._newColorant = { code: '', name: '' };
            await this._loadSystem();
        });
    }

    private async _addColor() {
        const n = this._newColor;
        await this._run('colors', 'Colour added', async () => {
            await tintService.createColor({
                tint_system_id: this._systemId, manufacturer_code: n.code.trim() || undefined, name: n.name.trim(),
            });
            this._newColor = { code: '', name: '' };
            await this._loadSystem();
        });
    }

    private async _find(base: TintBase, text: string) {
        this._search = { ...this._search, [base.id]: { text, found: this._search[base.id]?.found ?? [] } };
        try {
            const found = await posService.searchProducts(text.trim());
            this._search = { ...this._search, [base.id]: { text, found } };
        } catch (err) {
            this._note(`base:${base.id}`, 'error', apiErrorMessage(err, 'Could not search the products'));
        }
    }

    /** Links the base to the product, or clears the link when the product is null. */
    private async _link(base: TintBase, product: QuickSearchResult | null) {
        await this._run(`base:${base.id}`, product ? 'Base linked' : 'Base unlinked', async () => {
            const updated = await tintService.updateBase(base.id, { product_id: product ? product.product_id : null });
            this._bases = this._bases.map((b) => (b.id === base.id ? updated : b));
            this._search = { ...this._search, [base.id]: { text: '', found: [] } };
            if (product) this._labels = { ...this._labels, [product.product_id]: `${product.sku} ${product.description}` };
        });
    }

    private _setFormula(patch: Partial<GableTintCatalogue['_formula']>) {
        this._formula = { ...this._formula, ...patch };
    }

    private _addComponent() {
        const f = this._formula;
        this._formula = { ...f, components: [...f.components, { colorantId: this._colorants[0]?.id ?? '', whole: '0', coarse: '0', fine: '0' }] };
    }

    private _setComponent(i: number, patch: Partial<DraftComponent>) {
        const f = this._formula;
        this._formula = { ...f, components: f.components.map((c, j) => (j === i ? { ...c, ...patch } : c)) };
    }

    private async _addFormula() {
        const f = this._formula;
        const denominator = this._system?.unit_denominator ?? 0;
        await this._run('formulas', 'Formula added', async () => {
            await tintService.createFormula({
                color_id: f.colorId, base_id: f.baseId, container_size_code: f.size.trim().toUpperCase(),
                container_volume_oz: Number(f.volume), source: 'manual',
                components: f.components.map((c) => ({
                    colorant_id: c.colorantId, whole_oz: Number(c.whole), increments_coarse: Number(c.coarse),
                    increments_fine: Number(c.fine), unit_denominator: denominator,
                })),
            });
            this._formula = { ...f, components: [] };
        });
    }

    private _input(label: string, testid: string, value: string, set: (v: string) => void, mono = false) {
        return html`
            <label class="block space-y-1">
                <span class="text-xs uppercase tracking-wider text-slate-400">${label}</span>
                <input type="text" data-testid=${testid} .value=${value}
                    @input=${(e: Event) => set((e.target as HTMLInputElement).value)}
                    class="${FIELD_CLASS} ${mono ? 'font-mono' : ''}" />
            </label>`;
    }

    private _select(label: string, testid: string, value: string, options: { id: string; text: string }[], set: (v: string) => void) {
        return html`
            <div class="block space-y-1">
                <span class="text-xs uppercase tracking-wider text-slate-400">${label}</span>
                <gable-select data-testid=${testid} label=${label} placeholder="Choose" .value=${value}
                    .options=${options.map((o) => ({ value: o.id, label: o.text }))}
                    @change=${(e: Event) => set((e.target as HTMLSelectElement).value)}></gable-select>
            </div>`;
    }

    /** The outcome of the last action when it belongs to this card. */
    private _noteFor(card: string) {
        const n = this._notes[card];
        if (!n) return nothing;
        return n.kind === 'error'
            ? html`<div role="alert" data-testid="tint-error" data-card=${card}
                class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">${n.text}</div>`
            : html`<div role="status" data-testid="tint-success" data-card=${card}
                class="bg-gable-green/10 border border-gable-green/20 rounded-lg p-3 text-sm text-gable-green">${n.text}</div>`;
    }

    private _baseRow(b: TintBase) {
        const s = this._search[b.id] ?? { text: '', found: [] };
        const linked = b.product_id ? (this._labels[b.product_id] ?? 'A product is linked') : null;
        return html`
            <li class="py-3 space-y-2" data-testid="tint-base-${b.code}">
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                    <span class="text-sm text-white">${b.name} <span class="text-slate-500 font-mono">${b.code}</span>
                        ${b.is_deepest ? html`<span class="text-xs text-slate-400">deepest</span>` : nothing}</span>
                    <span class="text-xs text-slate-400 font-mono">${b.fill_volume_oz} oz</span>
                </div>
                <div class="flex flex-wrap items-center gap-2 text-sm">
                    <span class="text-slate-300" data-testid="tint-base-link-${b.code}">
                        ${linked ?? 'Not linked to a product: the counter cannot tint it'}</span>
                    ${b.product_id ? html`
                        <button class="text-xs text-slate-300 underline" data-testid="tint-base-unlink-${b.code}"
                            ?disabled=${this._busy} @click=${() => this._link(b, null)}>Unlink</button>` : nothing}
                </div>
                <div class="space-y-1">
                    <input type="text" placeholder="Find the product by SKU or name" data-testid="tint-base-find-${b.code}"
                        .value=${s.text} @input=${(e: Event) => this._find(b, (e.target as HTMLInputElement).value)}
                        class=${FIELD_CLASS} />
                    ${this._noteFor(`base:${b.id}`)}
                    ${s.found.length > 0 ? html`
                        <ul class="border border-white/10 rounded divide-y divide-white/5">
                            ${s.found.map((p) => html`
                                <li><button class="w-full text-left px-3 py-2 text-sm text-slate-200 hover:bg-white/5"
                                    data-testid="tint-base-pick-${b.code}-${p.sku}" ?disabled=${this._busy}
                                    @click=${() => this._link(b, p)}>${p.sku} ${p.description}</button></li>`)}
                        </ul>` : nothing}
                </div>
            </li>`;
    }

    render() {
        if (this._loading) return html`<div class="text-slate-400 p-8">Loading the tint catalogue...</div>`;
        const sys = this._system;
        const n = this._newSystem;
        const f = this._formula;
        const card = 'bg-slate-steel border border-white/10 rounded-lg p-6 space-y-4';
        return html`
            <div class="space-y-6">
                <div>
                    <h1 class="text-3xl font-bold text-white flex items-center gap-2">
                        ${icon(Palette, 24, 'w-6 h-6 text-gable-green')}
                        Tint catalogue
                    </h1>
                    <p class="text-slate-400 text-sm mt-1">
                        The tint systems the dealer runs, the base paints each one mixes in and the product each base is sold
                        as, the colorants, the colours and the formulas. A base with no product cannot be tinted at the counter.
                    </p>
                </div>
                ${this._noteFor('page')}

                <section class="${card}" data-testid="tint-systems">
                    <h2 class="text-lg font-semibold text-white">Tint system</h2>
                    ${this._systems.length > 0 ? this._select('System', 'tint-system-pick', this._systemId,
                        this._systems.map((s) => ({ id: s.id, text: `${s.name} (${s.code})` })), (v) => this._pickSystem(v)) : html`
                        <p class="text-sm text-slate-400">No tint system yet. Add the dealer's first one below.</p>`}
                    <div class="grid gap-3 sm:grid-cols-4">
                        ${this._input('Code', 'tint-new-system-code', n.code, (v) => { this._newSystem = { ...this._newSystem, code: v }; }, true)}
                        ${this._input('Name', 'tint-new-system-name', n.name, (v) => { this._newSystem = { ...this._newSystem, name: v }; })}
                        ${this._input('Manufacturer', 'tint-new-system-maker', n.manufacturer, (v) => { this._newSystem = { ...this._newSystem, manufacturer: v }; })}
                        ${this._input('Dispense units per ounce', 'tint-new-system-denominator', n.denominator, (v) => { this._newSystem = { ...this._newSystem, denominator: v }; }, true)}
                    </div>
                    <button class=${BUTTON_CLASS} data-testid="tint-add-system" ?disabled=${this._busy || n.code.trim() === '' || n.name.trim() === ''}
                        @click=${() => this._addSystem()}>Add tint system</button>
                    ${this._noteFor('system')}
                </section>

                ${sys ? html`
                <section class="${card}" data-testid="tint-bases">
                    <h2 class="text-lg font-semibold text-white">Bases and their products</h2>
                    ${this._bases.length === 0 ? html`<p class="text-sm text-slate-400">No bases yet.</p>` : html`
                        <ul class="divide-y divide-white/10">${this._bases.map((b) => this._baseRow(b))}</ul>`}
                    <div class="grid gap-3 sm:grid-cols-4">
                        ${this._input('Code', 'tint-new-base-code', this._newBase.code, (v) => { this._newBase = { ...this._newBase, code: v }; }, true)}
                        ${this._input('Name', 'tint-new-base-name', this._newBase.name, (v) => { this._newBase = { ...this._newBase, name: v }; })}
                        ${this._input('Fill (ounces)', 'tint-new-base-fill', this._newBase.fill, (v) => { this._newBase = { ...this._newBase, fill: v }; }, true)}
                        <label class="flex items-end gap-2 text-sm text-slate-200 pb-2">
                            <input type="checkbox" data-testid="tint-new-base-deepest" .checked=${this._newBase.deepest}
                                @change=${(e: Event) => { this._newBase = { ...this._newBase, deepest: (e.target as HTMLInputElement).checked }; }} />
                            Deepest base
                        </label>
                    </div>
                    <button class=${BUTTON_CLASS} data-testid="tint-add-base"
                        ?disabled=${this._busy || this._newBase.code.trim() === '' || this._newBase.name.trim() === ''}
                        @click=${() => this._addBase()}>Add base</button>
                    ${this._noteFor('bases')}
                </section>

                <section class="${card}" data-testid="tint-colorants">
                    <h2 class="text-lg font-semibold text-white">Colorants</h2>
                    <p class="text-sm text-slate-300" data-testid="tint-colorant-list">
                        ${this._colorants.length === 0 ? 'None yet.' : this._colorants.map((c) => `${c.name} (${c.code})`).join(', ')}</p>
                    <div class="grid gap-3 sm:grid-cols-2">
                        ${this._input('Code', 'tint-new-colorant-code', this._newColorant.code, (v) => { this._newColorant = { ...this._newColorant, code: v }; }, true)}
                        ${this._input('Name', 'tint-new-colorant-name', this._newColorant.name, (v) => { this._newColorant = { ...this._newColorant, name: v }; })}
                    </div>
                    <button class=${BUTTON_CLASS} data-testid="tint-add-colorant"
                        ?disabled=${this._busy || this._newColorant.code.trim() === '' || this._newColorant.name.trim() === ''}
                        @click=${() => this._addColorant()}>Add colorant</button>
                    ${this._noteFor('colorants')}
                </section>

                <section class="${card}" data-testid="tint-colors">
                    <h2 class="text-lg font-semibold text-white">Colours</h2>
                    <p class="text-sm text-slate-300" data-testid="tint-color-list">
                        ${this._colors.length === 0 ? 'None yet.' : this._colors.map((c) => c.manufacturer_code ? `${c.name} (${c.manufacturer_code})` : c.name).join(', ')}</p>
                    <div class="grid gap-3 sm:grid-cols-2">
                        ${this._input('Manufacturer code', 'tint-new-color-code', this._newColor.code, (v) => { this._newColor = { ...this._newColor, code: v }; }, true)}
                        ${this._input('Name', 'tint-new-color-name', this._newColor.name, (v) => { this._newColor = { ...this._newColor, name: v }; })}
                    </div>
                    <button class=${BUTTON_CLASS} data-testid="tint-add-color"
                        ?disabled=${this._busy || this._newColor.name.trim() === ''}
                        @click=${() => this._addColor()}>Add colour</button>
                    ${this._noteFor('colors')}
                </section>

                <section class="${card}" data-testid="tint-formulas">
                    <h2 class="text-lg font-semibold text-white">Add a formula</h2>
                    <p class="text-xs text-slate-500">One formula is a colour on a base in one container size. Amounts are in whole
                        ounces with the guide's two increment columns, in this system's ${sys.unit_denominator} per ounce.</p>
                    <div class="grid gap-3 sm:grid-cols-4">
                        ${this._select('Colour', 'tint-formula-color', f.colorId,
                            this._colors.map((c) => ({ id: c.id, text: c.name })), (v) => this._setFormula({ colorId: v }))}
                        ${this._select('Base', 'tint-formula-base', f.baseId,
                            this._bases.map((b) => ({ id: b.id, text: `${b.name} (${b.code})` })), (v) => this._setFormula({ baseId: v }))}
                        ${this._input('Size code', 'tint-formula-size', f.size, (v) => this._setFormula({ size: v }), true)}
                        ${this._input('Container (ounces)', 'tint-formula-volume', f.volume, (v) => this._setFormula({ volume: v }), true)}
                    </div>
                    ${f.components.map((c, i) => html`
                        <div class="grid gap-3 sm:grid-cols-4" data-testid="tint-formula-row-${i}">
                            ${this._select('Colorant', `tint-formula-colorant-${i}`, c.colorantId,
                                this._colorants.map((k) => ({ id: k.id, text: k.name })), (v) => this._setComponent(i, { colorantId: v }))}
                            ${this._input('Whole oz', `tint-formula-whole-${i}`, c.whole, (v) => this._setComponent(i, { whole: v }), true)}
                            ${this._input('Coarse', `tint-formula-coarse-${i}`, c.coarse, (v) => this._setComponent(i, { coarse: v }), true)}
                            ${this._input('Fine', `tint-formula-fine-${i}`, c.fine, (v) => this._setComponent(i, { fine: v }), true)}
                        </div>`)}
                    <div class="flex gap-2">
                        <button class="px-3 py-2 rounded border border-white/10 text-sm text-slate-200" data-testid="tint-formula-add-colorant"
                            ?disabled=${this._colorants.length === 0} @click=${() => this._addComponent()}>Add a colorant</button>
                        <button class=${BUTTON_CLASS} data-testid="tint-add-formula"
                            ?disabled=${this._busy || f.colorId === '' || f.baseId === '' || f.components.length === 0}
                            @click=${() => this._addFormula()}>Add formula</button>
                    </div>
                    ${this._noteFor('formulas')}
                </section>` : nothing}
            </div>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-tint-catalogue': GableTintCatalogue;
    }
}
