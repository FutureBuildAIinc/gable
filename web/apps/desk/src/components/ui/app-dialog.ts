// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The app's own dialog, in place of the browser's. Pages call the promise
 * helpers rather than the browser's blocking calls:
 *
 *   const ok = await confirmDialog({ title: 'Confirm this order?' });
 *   const reason = await promptDialog({ title: 'Cancel this order', label: 'Reason', required: true });
 *
 * One element is rendered over the page at a time; a second call settles the
 * one still open as cancelled. Focus moves to the safe choice (Cancel for a
 * danger tone, the field for a prompt, the confirm otherwise), Tab stays
 * inside, Escape cancels, Enter confirms when the focus is not on Cancel, and
 * the focus returns to the control that opened the dialog. A required prompt
 * keeps Confirm off until text is typed.
 *
 * The panel is a native <dialog> opened with showModal(): the browser's top
 * layer puts it above every page layer (a question asked from inside a modal,
 * like the fleet modals, stays answerable) and makes the page behind inert.
 * The dim veil stays a plain div under it, styled like the other modals'.
 * A page modal whose own veil paints above this one (the fleet modals) names
 * its panel with data-modal-panel, and this dialog dims that panel with a
 * scrim at the panel's own radius while the question is up.
 */
import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

export type DialogTone = 'default' | 'danger';
export type DialogMode = 'confirm' | 'prompt';

export interface ConfirmDialogOptions {
    title: string;
    body?: string;
    confirmLabel?: string;
    cancelLabel?: string;
    tone?: DialogTone;
}

export interface PromptDialogOptions {
    title: string;
    label?: string;
    body?: string;
    required?: boolean;
    confirmLabel?: string;
    cancelLabel?: string;
    initialValue?: string;
    placeholder?: string;
    tone?: DialogTone;
}

export interface DialogDoneDetail {
    confirmed: boolean;
    value: string | null;
}

let dialogSeq = 0;
let openDialog: GableAppDialog | null = null;

@customElement('gable-app-dialog')
export class GableAppDialog extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: String }) mode: DialogMode = 'confirm';
    @property({ type: String }) heading = '';
    @property({ type: String }) body = '';
    @property({ type: String, attribute: 'confirm-label' }) confirmLabel = 'Confirm';
    @property({ type: String, attribute: 'cancel-label' }) cancelLabel = 'Cancel';
    @property({ type: String }) tone: DialogTone = 'default';
    @property({ type: String, attribute: 'field-label' }) fieldLabel = '';
    @property({ type: Boolean }) required = false;
    @property({ type: String, attribute: 'initial-value' }) initialValue = '';
    @property({ type: String }) placeholder = '';

    @state() private _value = '';
    /** The panel-sized scrim over a modal open behind this dialog, if any. */
    @state() private _scrim: { top: number; left: number; width: number; height: number; radius: string } | null = null;

    private _ids = `gable-app-dialog-${++dialogSeq}`;
    private _returnFocus: HTMLElement | null = null;

    connectedCallback() {
        super.connectedCallback();
        this._returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        this._value = this.initialValue;
    }

    firstUpdated() {
        const dlg = this.querySelector('dialog');
        if (dlg && !dlg.open) {
            if (typeof dlg.showModal === 'function') {
                dlg.showModal();
            } else {
                // jsdom and older engines: no top layer, so the attribute alone.
                dlg.setAttribute('open', '');
            }
        }
        this._coverModalBehind();
        const target =
            this.mode === 'prompt'
                ? this.querySelector<HTMLElement>('input')
                : this.tone === 'danger'
                    ? this.querySelector<HTMLElement>('[data-dialog-cancel]')
                    : this.querySelector<HTMLElement>('[data-dialog-confirm]');
        target?.focus();
    }

    /** Settle the dialog without a choice; used when a newer dialog takes over. */
    dismiss() {
        this._finish(false);
    }

    private _settled = false;

    private _finish(confirmed: boolean) {
        if (this._settled) return;
        this._settled = true;
        const detail: DialogDoneDetail = {
            confirmed,
            value: confirmed && this.mode === 'prompt' ? this._value.trim() : null,
        };
        if (openDialog === this) openDialog = null;
        const dlg = this.querySelector('dialog');
        if (dlg?.open && typeof dlg.close === 'function') dlg.close();
        this.dispatchEvent(new CustomEvent<DialogDoneDetail>('done', { detail }));
        if (this._returnFocus?.isConnected) this._returnFocus.focus();
    }

    private _cancel() {
        this._finish(false);
    }

    private _confirm() {
        if (this.mode === 'prompt' && this.required && !this._value.trim()) return;
        this._finish(true);
    }

    private _focusables(): HTMLElement[] {
        return Array.from(
            this.querySelectorAll<HTMLElement>('[data-dialog-input], [data-dialog-cancel], [data-dialog-confirm]'),
        ).filter((el) => !(el as HTMLButtonElement).disabled);
    }

    /**
     * A page modal that can sit under this dialog names its panel with
     * data-modal-panel (its own veil paints above this dialog's, so the plain
     * veil cannot dim it): draw a scrim over the panel's own box, at its own
     * radius, under this dialog's top layer panel.
     */
    private _coverModalBehind() {
        const panels = Array.from(document.querySelectorAll<HTMLElement>('[data-modal-panel]'));
        const panel = panels[panels.length - 1];
        if (!panel) return;
        const r = panel.getBoundingClientRect();
        this._scrim = {
            top: r.top,
            left: r.left,
            width: r.width,
            height: r.height,
            radius: getComputedStyle(panel).borderRadius || panel.style.borderRadius || '0',
        };
    }

    private _onKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            this._cancel();
            return;
        }
        if (e.key === 'Tab') {
            const focusables = this._focusables();
            if (focusables.length === 0) return;
            const first = focusables[0];
            const last = focusables[focusables.length - 1];
            const active = document.activeElement;
            if (e.shiftKey && (active === first || !this.contains(active))) {
                e.preventDefault();
                last.focus();
            } else if (!e.shiftKey && (active === last || !this.contains(active))) {
                e.preventDefault();
                first.focus();
            }
            return;
        }
        if (e.key === 'Enter') {
            e.preventDefault();
            if (document.activeElement === this.querySelector('[data-dialog-cancel]')) {
                this._cancel();
            } else {
                this._confirm();
            }
        }
    }

    private _onDialogClick(e: MouseEvent) {
        // A click on the dialog element itself is a click on its backdrop.
        if (e.target === e.currentTarget) this._cancel();
    }

    private _onDialogCancel(e: Event) {
        // Escape on a modal dialog fires cancel; route it through our settle.
        e.preventDefault();
        this._cancel();
    }

    render() {
        const promptInvalid = this.mode === 'prompt' && this.required && !this._value.trim();
        const describedBy = this.body ? `${this._ids}-body` : nothing;
        return html`
            <div class="relative z-50" @keydown=${this._onKeydown}>
                <style>dialog::backdrop { background: transparent; }</style>
                <div class="fixed inset-0 bg-backdrop/80 backdrop-blur-sm" @click=${this._cancel}></div>
                <dialog
                    role=${this.mode === 'prompt' ? 'dialog' : 'alertdialog'}
                    aria-modal="true"
                    aria-labelledby="${this._ids}-title"
                    aria-describedby=${describedBy}
                    @click=${this._onDialogClick}
                    @cancel=${this._onDialogCancel}
                    class="fixed inset-0 m-auto w-full max-w-md h-fit max-h-[85vh] overflow-y-auto rounded-2xl bg-slate-steel border border-white/10 p-0 shadow-xl text-left"
                >
                    <div class="p-6">
                        <h2 id="${this._ids}-title" class="text-lg font-bold text-white">${this.heading}</h2>
                        ${this.body
                            ? html`<p id="${this._ids}-body" class="mt-2 text-sm text-zinc-400 whitespace-pre-line">${this.body}</p>`
                            : nothing}
                        ${this.mode === 'prompt'
                            ? html`
                                <div class="mt-4">
                                    ${this.fieldLabel
                                        ? html`<label for="${this._ids}-field" class="block text-xs font-medium text-zinc-400 mb-1">${this.fieldLabel}</label>`
                                        : nothing}
                                    <input
                                        id="${this._ids}-field"
                                        data-dialog-input
                                        type="text"
                                        .value=${this._value}
                                        placeholder=${this.placeholder}
                                        @input=${(e: Event) => { this._value = (e.target as HTMLInputElement).value; }}
                                        class="w-full bg-slate-steel/50 border border-white/10 rounded-md py-2 px-3 text-sm text-white placeholder-zinc-600 focus:outline-none focus:ring-1 focus:ring-gable-green/50"
                                    />
                                </div>
                            `
                            : nothing}
                        <div class="mt-6 flex justify-end gap-3">
                            <button
                                type="button"
                                data-dialog-cancel
                                @click=${this._cancel}
                                class="inline-flex items-center gap-2 px-3 py-1.5 border border-white/10 text-zinc-300 hover:text-white text-sm font-medium rounded-md transition-colors"
                            >${this.cancelLabel}</button>
                            <button
                                type="button"
                                data-dialog-confirm
                                ?disabled=${promptInvalid}
                                @click=${this._confirm}
                                class="inline-flex items-center gap-2 px-4 py-2 text-sm font-semibold rounded-md transition-colors disabled:opacity-50 ${this.tone === 'danger'
                                    ? 'bg-safety-red text-white hover:bg-safety-red/90'
                                    : 'bg-gable-green text-black hover:bg-gable-green/90'}"
                            >${this.confirmLabel}</button>
                        </div>
                    </div>
                </dialog>
            </div>
            ${this._scrim
                ? html`
                    <div
                        data-dialog-scrim
                        aria-hidden="true"
                        style="position: fixed; z-index: 70; top: ${this._scrim.top}px; left: ${this._scrim.left}px; width: ${this._scrim.width}px; height: ${this._scrim.height}px; border-radius: ${this._scrim.radius};"
                        class="bg-backdrop/60 backdrop-blur-sm pointer-events-none"
                    ></div>
                `
                : nothing}
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-app-dialog': GableAppDialog;
    }
}

function runDialog(assign: (el: GableAppDialog) => void): Promise<DialogDoneDetail> {
    if (openDialog) openDialog.dismiss();
    return new Promise((resolve) => {
        const el = document.createElement('gable-app-dialog') as GableAppDialog;
        assign(el);
        el.addEventListener('done', (e) => {
            el.remove();
            resolve((e as CustomEvent<DialogDoneDetail>).detail);
        });
        openDialog = el;
        document.body.appendChild(el);
    });
}

/** Ask the person to confirm or cancel; resolves true on confirm, false otherwise. */
export function confirmDialog(options: ConfirmDialogOptions): Promise<boolean> {
    return runDialog((el) => {
        el.mode = 'confirm';
        el.heading = options.title;
        el.body = options.body ?? '';
        el.confirmLabel = options.confirmLabel ?? 'Confirm';
        el.cancelLabel = options.cancelLabel ?? 'Cancel';
        el.tone = options.tone ?? 'default';
    }).then((detail) => detail.confirmed);
}

/** Ask the person for a line of text; resolves the typed text or null on cancel. */
export function promptDialog(options: PromptDialogOptions): Promise<string | null> {
    return runDialog((el) => {
        el.mode = 'prompt';
        el.heading = options.title;
        el.body = options.body ?? '';
        el.fieldLabel = options.label ?? '';
        el.required = options.required ?? false;
        el.confirmLabel = options.confirmLabel ?? 'Confirm';
        el.cancelLabel = options.cancelLabel ?? 'Cancel';
        el.initialValue = options.initialValue ?? '';
        el.placeholder = options.placeholder ?? '';
        el.tone = options.tone ?? 'default';
    }).then((detail) => (detail.confirmed ? detail.value : null));
}
