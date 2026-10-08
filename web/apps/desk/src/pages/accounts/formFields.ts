// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Small form helpers shared by the account pages, so every input has a label bound by id,
// the same dark look, and the server's field problem shown beside it.

import { html, nothing, type TemplateResult } from 'lit';

export const INPUT_CLASS =
    'w-full bg-slate-steel/50 border border-white/10 rounded-md py-2 px-3 text-sm text-white placeholder-zinc-600 focus:outline-none focus:ring-1 focus:ring-gable-green/50';
export const BUTTON_PRIMARY =
    'inline-flex items-center gap-2 px-4 py-2 bg-gable-green text-black text-sm font-semibold rounded-md hover:shadow-glow disabled:opacity-50 transition-all';
export const BUTTON_QUIET =
    'inline-flex items-center gap-2 px-3 py-1.5 border border-white/10 text-zinc-300 hover:text-white text-sm font-medium rounded-md transition-colors disabled:opacity-50';

let seq = 0;
/** A fresh id prefix per component instance, so two forms on a page never share an id. */
export function idPrefix(name: string): string {
    seq += 1;
    return `${name}-${seq}`;
}

/** A label bound to its control by id, with the field's server problem under it. */
export function labeled(id: string, label: string, control: TemplateResult, error?: string, hint?: string): TemplateResult {
    return html`
        <div>
            <label for=${id} class="block text-xs font-medium text-zinc-400 mb-1">${label}</label>
            ${control}
            ${hint && !error ? html`<p class="text-xs text-zinc-500 mt-1">${hint}</p>` : nothing}
            ${error ? html`<p class="text-xs text-red-400 mt-1" role="alert" data-field-error>${error}</p>` : nothing}
        </div>
    `;
}

export function textInput(
    id: string,
    value: string,
    onInput: (v: string) => void,
    opts: { type?: string; required?: boolean; maxlength?: number; placeholder?: string; mono?: boolean; inputmode?: string } = {},
): TemplateResult {
    return html`<input
        id=${id}
        type=${opts.type ?? 'text'}
        ?required=${opts.required}
        maxlength=${opts.maxlength ?? 200}
        placeholder=${opts.placeholder ?? ''}
        inputmode=${opts.inputmode ?? 'text'}
        .value=${value}
        @input=${(e: Event) => onInput((e.target as HTMLInputElement).value)}
        class="${INPUT_CLASS} ${opts.mono ? 'font-mono' : ''}"
    />`;
}

export function checkbox(id: string, label: string, checked: boolean, onChange: (v: boolean) => void): TemplateResult {
    return html`
        <label for=${id} class="flex items-center gap-2 text-sm text-zinc-300">
            <input
                id=${id}
                type="checkbox"
                .checked=${checked}
                @change=${(e: Event) => onChange((e.target as HTMLInputElement).checked)}
                class="h-4 w-4 rounded border-white/20 bg-slate-steel/50"
            />
            ${label}
        </label>
    `;
}

/** Empty text is null on the wire, the way an optional field is read back. */
export function orNull(text: string): string | null {
    const t = text.trim();
    return t === '' ? null : t;
}
