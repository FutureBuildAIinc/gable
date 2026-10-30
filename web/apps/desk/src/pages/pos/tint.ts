// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The tint dialog's pure helpers (row CP-16): the guide layout printer, the
// editable amount state, and the price the dialog shows (the base price plus
// the dealer's tint charge). No floats in the arithmetic: the amounts are
// integer counts of a named dispense unit.

import type { TintAmount, TintComponent } from '../../types/pos.ts';

/** The amount as a whole count of its own denominator. */
export function fineUnits(a: TintAmount): number {
    const coarse = a.unit_denominator % 32 === 0 ? a.increments_coarse * (a.unit_denominator / 32) : 0;
    return a.whole_oz * a.unit_denominator + coarse + a.increments_fine;
}

/** Prints the amount in the manufacturer's own layout, readable against the book. */
export function formatAmount(a: TintAmount): string {
    const parts: string[] = [];
    if (a.whole_oz > 0) parts.push(`${a.whole_oz} OZ`);
    if (a.increments_coarse > 0) parts.push(`${a.increments_coarse}/32`);
    if (a.increments_fine > 0) parts.push(`${a.increments_fine}/${a.unit_denominator}`);
    return parts.length ? parts.join(' + ') : '0';
}

/** A typed amount for editing: whole, coarse and fine counts in one grid. */
export function editableAmount(a: TintAmount): TintAmount {
    return { whole_oz: a.whole_oz, increments_coarse: a.increments_coarse, increments_fine: a.increments_fine, unit_denominator: a.unit_denominator };
}

/** The tint's colorant total in the system's whole dispense units, and the grid they are counted on. */
export function colorantUnits(components: Array<{ amount: TintAmount }>, systemDenominator = 384): { units: number; denominator: number } {
    const first = components[0];
    if (!first) return { units: 0, denominator: systemDenominator > 0 ? systemDenominator : 384 };
    const denominator = first.amount.unit_denominator;
    let units = 0;
    for (const c of components) {
        // a component on another grid is not added to this one: the sum of two
        // grids is not a number, and the server reads the tint's own amounts
        if (c.amount.unit_denominator !== denominator) continue;
        units += fineUnits(c.amount);
    }
    return { units, denominator };
}

/** The unit price the line takes, in ten thousandths: the base price plus the flat tint charge (0 when none is set). */
export function priceWithTintCharge(basePriceTenThousandths: number, tintChargeCents: number): number {
    return basePriceTenThousandths + tintChargeCents * 100;
}

/** The line request's tint payload the dialog sends, from the chosen pieces. */
export function tintPayload(args: {
    formula_id: string | null;
    color_code: string;
    color_name: string;
    base_code: string;
    container_size_code: string;
    rescaled: boolean;
    tint_charge_cents: number | null;
    components: Array<{ colorant_code: string; colorant_name: string; amount: TintAmount }>;
}) {
    return {
        formula_id: args.formula_id,
        color_code: args.color_code,
        color_name: args.color_name,
        base_code: args.base_code,
        container_size_code: args.container_size_code,
        rescaled: args.rescaled,
        tint_charge_cents: args.tint_charge_cents,
        components: args.components.map((c) => ({
            colorant_code: c.colorant_code,
            colorant_name: c.colorant_name,
            amount: editableAmount(c.amount),
        })),
    };
}

/** The one line summary the cart shows under a tinted line. */
export function tintSummary(components: TintComponent[]): string {
    return components.map((c) => `${c.colorant_code} ${formatAmount(c.amount)}`).join(', ');
}
