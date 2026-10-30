// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { WifiOff } from 'lucide';
import { apiErrorMessage } from '../../services/apiError';
import { dollarsToCents } from '../../services/POSService';
import { canManageOffline, offlineAdminService } from '../../services/OfflineAdminService';
import type { OfflinePolicy, OfflinePolicyInput, OfflineTender } from '../../services/OfflineAdminService';
import { branchContext } from '../../services/BranchContext';
import { onBranchChanged } from '../../lib/branch-listener';
import { formatCents } from '../../lib/utils';
import './RegisterDevices.ts';

interface Draft {
    enabled: boolean;
    tenders: OfflineTender[];
    saleCap: string;
    checkCap: string;
    accountCap: string;
    queueCap: string;
    maxQueued: string;
}

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green';

const TENDERS: { value: OfflineTender; label: string }[] = [
    { value: 'cash', label: 'Cash' },
    { value: 'check', label: 'Checks' },
    { value: 'account', label: 'On account' },
];

/** The dollars text a stored cents figure shows. */
export function dollarsText(cents: number): string {
    return `${Math.trunc(cents / 100)}.${String(cents % 100).padStart(2, '0')}`;
}

export function draftOf(p: OfflinePolicy): Draft {
    return {
        enabled: p.enabled,
        tenders: [...p.tenders],
        saleCap: dollarsText(p.sale_cap_cents),
        checkCap: dollarsText(p.check_cap_cents),
        accountCap: dollarsText(p.account_cap_cents),
        queueCap: dollarsText(p.queue_total_cap_cents),
        maxQueued: String(p.max_queued),
    };
}

/** The draft as the wire's policy, or the field the clerk must fix. */
export function policyOf(d: Draft): { ok: true; input: OfflinePolicyInput } | { ok: false; message: string } {
    const caps: [string, string][] = [
        [d.saleCap, 'The cap on one sale'],
        [d.checkCap, 'The cap on a check'],
        [d.accountCap, 'The cap per account'],
        [d.queueCap, 'The cap on the whole queue'],
    ];
    const cents: number[] = [];
    for (const [text, label] of caps) {
        const c = dollarsToCents(text);
        if (isNaN(c) || c < 0) return { ok: false, message: `${label} must be a dollar amount, 0 or more` };
        cents.push(c);
    }
    if (!/^\d{1,6}$/.test(d.maxQueued.trim())) return { ok: false, message: 'The most waiting sales must be a whole number from 0 to 100000' };
    const max = Number(d.maxQueued.trim());
    if (max > 100000) return { ok: false, message: 'The most waiting sales must be a whole number from 0 to 100000' };
    if (d.enabled && d.tenders.length === 0) return { ok: false, message: 'Choose at least one tender the register may take offline' };
    return {
        ok: true,
        input: {
            enabled: d.enabled,
            tenders: d.tenders,
            sale_cap_cents: cents[0],
            check_cap_cents: cents[1],
            account_cap_cents: cents[2],
            queue_total_cap_cents: cents[3],
            max_queued: max,
        },
    };
}

/**
 * The counter's offline settings (ADR 0011 section 4) for the yard the desk is
 * working: whether registers may sell while offline, which tenders, the caps,
 * and the worst case on one account. Offline selling is off until a manager
 * turns it on. The registers' devices sit beneath it. Owner and admin write;
 * finance may read.
 */
@customElement('gable-offline-counter')
export class GableOfflineCounter extends LitElement {
    createRenderRoot() { return this; }

    @state() private policy: OfflinePolicy | null = null;
    @state() private draft: Draft | null = null;
    @state() private loading = true;
    @state() private saving = false;
    @state() private error = '';
    @state() private success = '';

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

    private adopt(p: OfflinePolicy) {
        this.policy = p;
        this.draft = draftOf(p);
    }

    private async load() {
        const id = branchContext.currentId;
        const seq = ++this.seq;
        this.error = '';
        this.success = '';
        if (!id) {
            this.policy = null;
            this.draft = null;
            this.loading = false;
            return;
        }
        this.loading = true;
        try {
            const p = await offlineAdminService.policy(id);
            if (seq === this.seq) this.adopt(p);
        } catch (err: unknown) {
            if (seq !== this.seq) return;
            this.policy = null;
            this.draft = null;
            this.error = apiErrorMessage(err, 'Could not read the offline settings');
        } finally {
            if (seq === this.seq) this.loading = false;
        }
    }

    private async save() {
        const d = this.draft;
        const id = branchContext.currentId;
        if (!d || !id || this.saving) return;
        this.error = '';
        this.success = '';
        const parsed = policyOf(d);
        if (!parsed.ok) {
            this.error = parsed.message;
            return;
        }
        this.saving = true;
        try {
            this.adopt(await offlineAdminService.savePolicy(id, parsed.input));
            this.success = 'Offline settings saved';
        } catch (err: unknown) {
            this.error = apiErrorMessage(err, 'Could not save the offline settings');
        } finally {
            this.saving = false;
        }
    }

    private async useDefaults() {
        const id = branchContext.currentId;
        if (!id || this.saving) return;
        this.error = '';
        this.success = '';
        this.saving = true;
        try {
            this.adopt(await offlineAdminService.clearPolicy(id));
            this.success = 'This yard now follows the dealer wide settings';
        } catch (err: unknown) {
            this.error = apiErrorMessage(err, 'Could not clear the offline settings');
        } finally {
            this.saving = false;
        }
    }

    private capField(label: string, key: 'saleCap' | 'checkCap' | 'accountCap' | 'queueCap', testid: string, hint?: unknown) {
        const d = this.draft as Draft;
        return html`
            <label class="block">
                <span class="block text-sm text-slate-300 mb-1">${label}</span>
                <input type="text" inputmode="decimal" data-testid=${testid} class=${FIELD_CLASS} .value=${d[key]}
                    @input=${(e: Event) => { this.draft = { ...d, [key]: (e.target as HTMLInputElement).value }; }} />
                ${hint ?? nothing}
            </label>`;
    }

    render() {
        if (!branchContext.currentId && !this.loading) {
            return html`<div class="text-slate-400 p-8" data-testid="offline-counter-none">Choose a yard to set its offline selling.</div>`;
        }
        if (this.loading) return html`<div class="text-slate-400 p-8">Loading the offline settings...</div>`;
        const p = this.policy;
        const d = this.draft;
        if (!p || !d) {
            return html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" role="alert" data-testid="offline-counter-error">${this.error}</div>`;
        }
        const accountCents = dollarsToCents(d.accountCap);
        const exposure = isNaN(accountCents) ? p.max_account_exposure_cents : accountCents * p.enrolled_register_count;
        const sourceWords = p.source === 'branch' ? 'This yard has its own settings.'
            : p.source === 'default' ? 'This yard follows the dealer wide settings. Saving gives it its own.'
            : 'Offline selling is off: nothing has been set for this yard.';
        return html`
            <div class="space-y-6">
                <div>
                    <h1 class="text-3xl font-bold text-white flex items-center gap-2">
                        ${icon(WifiOff, 24, 'w-6 h-6 text-gable-green')}
                        Offline selling
                    </h1>
                    <p class="text-slate-400 text-sm mt-1">
                        When the connection drops, an enrolled register can keep selling and send the sales when it
                        returns. Offline sales use the prices and tax the register held; promotions, quantity breaks and
                        job prices are not applied. Money never queues unless this is on.
                    </p>
                    <p class="text-xs text-slate-500 mt-1" data-testid="offline-counter-source">${sourceWords}</p>
                </div>

                <div class="bg-slate-900/50 border border-white/10 rounded-lg p-6 space-y-6" data-testid="offline-counter-form">
                    <label class="flex items-center gap-2 text-sm text-slate-200">
                        <input type="checkbox" data-testid="offline-enabled" .checked=${d.enabled} ?disabled=${!canManageOffline()}
                            @change=${(e: Event) => { this.draft = { ...d, enabled: (e.target as HTMLInputElement).checked }; }} />
                        Registers at this yard may sell while offline
                    </label>

                    <fieldset class="space-y-2" data-testid="offline-tenders" ?disabled=${!canManageOffline()}>
                        <legend class="text-sm font-semibold text-slate-200 mb-1">Taken offline</legend>
                        <p class="text-xs text-slate-500">Cards and store credit need the connection and cannot be taken offline.</p>
                        ${TENDERS.map((t) => html`
                            <label class="flex items-center gap-2 text-sm text-slate-200">
                                <input type="checkbox" data-testid="offline-tender-${t.value}" .checked=${d.tenders.includes(t.value)}
                                    @change=${(e: Event) => {
                                        const on = (e.target as HTMLInputElement).checked;
                                        this.draft = { ...d, tenders: on ? [...d.tenders, t.value] : d.tenders.filter((x) => x !== t.value) };
                                    }} />
                                ${t.label}
                            </label>`)}
                    </fieldset>

                    <fieldset class="grid grid-cols-1 sm:grid-cols-2 gap-4" data-testid="offline-caps" ?disabled=${!canManageOffline()}>
                        <legend class="text-sm font-semibold text-slate-200 mb-2">Limits</legend>
                        ${this.capField('Most on one sale', 'saleCap', 'offline-cap-sale')}
                        ${this.capField('Most on one check', 'checkCap', 'offline-cap-check')}
                        ${this.capField('Most on one account', 'accountCap', 'offline-cap-account', html`
                            <span class="block text-xs text-slate-500 mt-1" data-testid="offline-exposure">
                                Worst case on one account is this limit times the registers offline at once:
                                <span class="font-mono text-slate-300">${formatCents(exposure)}</span>
                                for the ${p.enrolled_register_count} enrolled register${p.enrolled_register_count === 1 ? '' : 's'} at this yard.
                            </span>`)}
                        ${this.capField('Most in one register\'s waiting sales', 'queueCap', 'offline-cap-queue')}
                        <label class="block">
                            <span class="block text-sm text-slate-300 mb-1">Most sales waiting on one register</span>
                            <input type="text" inputmode="numeric" data-testid="offline-max-queued" class=${FIELD_CLASS} .value=${d.maxQueued}
                                @input=${(e: Event) => { this.draft = { ...d, maxQueued: (e.target as HTMLInputElement).value }; }} />
                        </label>
                    </fieldset>

                    ${canManageOffline() ? html`<div class="flex flex-wrap justify-end gap-3">
                        ${p.source === 'branch' ? html`
                            <button type="button" data-testid="offline-use-defaults" @click=${() => void this.useDefaults()} ?disabled=${this.saving}
                                class="inline-flex items-center rounded border border-white/20 px-4 py-2 text-sm font-semibold text-slate-200 hover:bg-white/5 disabled:opacity-50">
                                Follow the dealer wide settings
                            </button>` : nothing}
                        <button type="button" data-testid="offline-save" @click=${() => void this.save()} ?disabled=${this.saving}
                            class="inline-flex items-center gap-2 bg-gable-green text-black font-semibold px-4 py-2 rounded disabled:opacity-50">
                            ${this.saving ? 'Saving...' : 'Save'}
                        </button>
                    </div>` : html`<p class="text-xs text-slate-500" data-testid="offline-counter-readonly">Only an owner or admin changes these settings.</p>`}
                </div>

                ${this.error ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" role="alert" data-testid="offline-counter-error">${this.error}</div>` : nothing}
                ${this.success ? html`<div class="bg-emerald-500/10 border border-emerald-500/20 rounded-lg p-3 text-sm text-emerald-400" data-testid="offline-counter-saved">${this.success}</div>` : nothing}

                <gable-register-devices class="block"></gable-register-devices>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-offline-counter': GableOfflineCounter;
    }
}
