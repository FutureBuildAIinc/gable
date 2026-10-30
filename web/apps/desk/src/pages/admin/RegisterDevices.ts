// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { canManageOffline, offlineAdminService, shellDevicePublicKey } from '../../services/OfflineAdminService.ts';
import type { RegisterDevice, RegisterSessions } from '../../services/OfflineAdminService.ts';
import { apiErrorMessage } from '../../services/apiError.ts';
import { branchContext } from '../../services/BranchContext.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatDateTime } from '../../lib/dates.ts';

type Act = 'enrol' | 'revoke';

/**
 * The registers' offline devices (ADR 0011 section 6.1), under the offline
 * settings. A manager enrols or revokes a register by typing their staff
 * number and own counter PIN: the proof they stand at the register. An
 * enrolment starts a new epoch and revokes the earlier key; a revoked
 * register's later offline sales go to the exception list, never dropped.
 * A yard with no registers lists none. Owner, admin and finance read the list;
 * only owner and admin enrol or revoke, so finance sees no buttons.
 */
@customElement('gable-register-devices')
export class GableRegisterDevices extends LitElement {
    createRenderRoot() { return this; }

    @state() private devices: RegisterDevice[] = [];
    @state() private loading = true;
    @state() private unavailable = false;
    @state() private error = '';
    @state() private busy = false;
    /** The register and act the PIN form is open for. */
    @state() private target: { registerId: string; act: Act } | null = null;
    @state() private manager = '';
    @state() private pin = '';
    /** Why a register is revoked: the audit row keeps it. */
    @state() private reason = '';
    /** What a revoke did to each register's web sessions, kept to show beside it. */
    @state() private sessionNotes: Record<string, RegisterSessions> = {};

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
        this.error = '';
        this.target = null;
        this.unavailable = false;
        if (!id) {
            this.devices = [];
            this.loading = false;
            return;
        }
        this.loading = true;
        try {
            const list = await offlineAdminService.devices(id);
            if (seq === this.seq) this.devices = list;
        } catch (err: unknown) {
            if (seq !== this.seq) return;
            this.devices = [];
            this.unavailable = true;
            this.error = apiErrorMessage(err, 'Failed to load the registers');
        } finally {
            if (seq === this.seq) this.loading = false;
        }
    }

    private begin(registerId: string, act: Act) {
        this.target = { registerId, act };
        this.manager = '';
        this.pin = '';
        this.reason = '';
        this.error = '';
    }

    private async confirm() {
        const t = this.target;
        if (!t || this.busy) return;
        if (this.manager.trim() === '' || this.pin === '') {
            this.error = 'Type your staff number and your counter PIN.';
            return;
        }
        if (t.act === 'revoke' && this.reason.trim() === '') {
            this.error = 'Say why the register is revoked.';
            return;
        }
        const current = this.devices.find((d) => d.register_id === t.registerId);
        if (!current) return;
        const proof = { manager: this.manager.trim(), pin: this.pin };
        try {
            this.busy = true;
            this.error = '';
            let device: RegisterDevice;
            if (t.act === 'enrol') {
                const key = await shellDevicePublicKey();
                if (key === null) {
                    this.error = "Enrol from the register's own work shell: only it holds the register's key.";
                    return;
                }
                device = await offlineAdminService.enrolDevice(t.registerId, current.next_epoch, key, proof);
            } else {
                device = await offlineAdminService.revokeDevice(t.registerId, current.epoch, this.reason.trim(), proof);
            }
            this.devices = this.devices.map((d) => (d.register_id === device.register_id ? device : d));
            const notes = { ...this.sessionNotes };
            if (device.sessions) notes[device.register_id] = device.sessions;
            else delete notes[device.register_id];
            this.sessionNotes = notes;
            this.target = null;
            ToastService.show(`${t.registerId} ${t.act === 'enrol' ? 'enrolled' : 'revoked'}`, 'success');
        } catch (err: unknown) {
            this.error = apiErrorMessage(err, 'The register was not changed');
        } finally {
            // the PIN is never kept after an attempt
            this.pin = '';
            this.busy = false;
        }
    }

    /** A session note is a warning unless the server ended the sessions cleanly. */
    private sessionWarns(s: RegisterSessions): boolean {
        return !s.supported || !/^Ended \d+ web sessions/.test(s.message);
    }

    private statusWords(d: RegisterDevice): string {
        if (d.status === 'active') return `Enrolled (epoch ${d.epoch})`;
        if (d.status === 'revoked') return `Revoked${d.revoked_at ? ` ${formatDateTime(d.revoked_at)}` : ''}`;
        return 'Not enrolled';
    }

    private renderForm(registerId: string, act: Act) {
        return html`
            <div class="mt-3 p-3 bg-black/20 rounded border border-white/5 space-y-3" data-testid="device-proof">
                <p class="text-sm text-zinc-300">
                    ${act === 'enrol'
                        ? 'Enrolling gives this register a new key and ends the old one. Offline sales it signed before are still sent.'
                        : 'Revoking ends this register\'s key. Its later offline sales go to a manager, never dropped.'}
                    Prove you are at the register with your own counter PIN.
                </p>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <label class="block">
                        <span class="block text-sm text-zinc-400 mb-1">Staff number</span>
                        <input type="text" data-testid="device-manager" autocomplete="off" .value=${this.manager}
                            @input=${(e: Event) => { this.manager = (e.target as HTMLInputElement).value; this.error = ''; }}
                            class="w-full px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white font-mono" />
                    </label>
                    <label class="block">
                        <span class="block text-sm text-zinc-400 mb-1">Counter PIN</span>
                        <input type="password" inputmode="numeric" data-testid="device-pin" autocomplete="off" maxlength="8" .value=${this.pin}
                            @input=${(e: Event) => { this.pin = (e.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 8); this.error = ''; }}
                            class="w-full px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white font-mono" />
                    </label>
                </div>
                ${act === 'revoke' ? html`
                    <label class="block">
                        <span class="block text-sm text-zinc-400 mb-1">Why</span>
                        <input type="text" data-testid="device-reason" maxlength="200" .value=${this.reason}
                            @input=${(e: Event) => { this.reason = (e.target as HTMLInputElement).value; this.error = ''; }}
                            class="w-full px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white" />
                    </label>` : nothing}
                <div class="flex gap-3">
                    <button type="button" data-testid="device-confirm" @click=${() => void this.confirm()} ?disabled=${this.busy}
                        class="px-4 py-2 rounded ${act === 'revoke' ? 'bg-red-500 text-white' : 'bg-emerald-500 text-black'} text-sm font-bold disabled:opacity-40">
                        ${act === 'enrol' ? 'Enrol' : 'Revoke'} ${registerId}</button>
                    <button type="button" data-testid="device-cancel" @click=${() => { this.target = null; this.pin = ''; this.error = ''; }}
                        class="px-4 py-2 rounded border border-zinc-700 text-zinc-200 text-sm">Cancel</button>
                </div>
            </div>`;
    }

    render() {
        if (!branchContext.currentId || this.loading) return nothing;
        if (this.unavailable) return nothing;
        return html`
            <div class="bg-slate-900/50 border border-white/10 rounded-lg p-6" data-testid="register-devices">
                <h2 class="text-xl font-bold text-white mb-1">Registers</h2>
                <p class="text-sm text-slate-400 mb-4">A register sells offline only while it is enrolled. Enrol from the register's own work shell.</p>
                ${this.devices.length === 0 ? html`
                    <p class="text-zinc-500" data-testid="register-devices-none">No register has opened a drawer at this yard yet.</p>` : html`
                    <ul class="space-y-3">
                        ${this.devices.map((d) => html`
                            <li class="p-4 bg-black/20 rounded border border-white/5" data-testid="device-${d.register_id}">
                                <div class="flex flex-wrap items-center justify-between gap-3">
                                    <div>
                                        <p class="text-white font-bold font-mono">${d.register_id}</p>
                                        <p class="text-sm ${d.status === 'active' ? 'text-emerald-400' : d.status === 'revoked' ? 'text-red-400' : 'text-zinc-400'}" data-testid="device-status">${this.statusWords(d)}</p>
                                        ${d.enrolled_by ? html`<p class="text-xs text-zinc-500">Enrolled by ${d.enrolled_by}${d.enrolled_at ? ` on ${formatDateTime(d.enrolled_at)}` : ''}</p>` : nothing}
                                    </div>
                                    ${this.target?.registerId === d.register_id || !canManageOffline() ? nothing : html`
                                        <div class="flex gap-3">
                                            <button type="button" data-testid="device-enrol" @click=${() => this.begin(d.register_id, 'enrol')}
                                                class="px-4 py-2 rounded border border-zinc-700 text-zinc-200 text-sm font-bold">${d.status === 'active' ? 'Enrol again' : 'Enrol'}</button>
                                            ${d.status === 'active' ? html`
                                                <button type="button" data-testid="device-revoke" @click=${() => this.begin(d.register_id, 'revoke')}
                                                    class="px-4 py-2 rounded border border-red-500/40 text-red-400 text-sm font-bold">Revoke</button>` : nothing}
                                        </div>`}
                                </div>
                                ${this.sessionNotes[d.register_id] ? html`
                                    <p class="mt-2 text-sm ${this.sessionWarns(this.sessionNotes[d.register_id]) ? 'text-amber-500' : 'text-zinc-400'}" data-testid="device-sessions">${this.sessionNotes[d.register_id].message}</p>` : nothing}
                                ${this.target?.registerId === d.register_id ? this.renderForm(d.register_id, this.target.act) : nothing}
                            </li>`)}
                    </ul>`}
                ${this.error ? html`<p class="mt-4 text-red-400 text-sm" role="alert" data-testid="register-devices-error">${this.error}</p>` : nothing}
            </div>`;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-register-devices': GableRegisterDevices;
    }
}
