// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import '../../components/ui/gable-select.ts';
import { branchContext } from '../../services/BranchContext';

/**
 * The accounting pages' branch scope: one branch or the consolidated view
 * (the sum of the branches). It is the slice of the books a statement shows,
 * not the operator's own yard. A choice fires a bubbling `branch-scope-change`
 * event whose detail carries the branch id, empty for the consolidated view.
 */
@customElement('gable-branch-scope')
export class BranchScope extends LitElement {
    createRenderRoot() { return this; }

    /** The selected branch id, empty for the consolidated view. */
    @property({ type: String }) value = '';

    private _choose(value: string) {
        this.value = value;
        this.dispatchEvent(new CustomEvent('branch-scope-change', { detail: { value }, bubbles: true }));
    }

    render() {
        const branches = branchContext.branches;
        if (branches.length === 0) return html``;
        const options = [{ value: '', label: 'Consolidated' }, ...branches.map(b => ({ value: b.id, label: b.name }))];
        return html`
            <label class="text-sm text-zinc-400 flex items-center gap-2">
                Branch:
                <gable-select
                    label="Branch"
                    .value=${this.value}
                    .options=${options}
                    @change=${(e: Event) => this._choose((e.target as HTMLSelectElement).value)}
                ></gable-select>
            </label>
        `;
    }
}

export default BranchScope;
