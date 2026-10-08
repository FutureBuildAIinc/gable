// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { html, nothing, type TemplateResult } from 'lit';
import { formatCents, formatPrice4 } from '../../lib/utils.ts';
import { creditTextClass, formatQuantity } from '../../lib/credit-display.ts';

/** The fields of the shared sales line shape this table reads; an invoice line and a credit memo line both fit. */
export interface SalesLineView {
    id: string;
    position: number;
    line_type: 'product' | 'kit' | 'component' | 'charge' | 'text';
    parent_line_id: string | null;
    charge_code: string | null;
    sku: string | null;
    description: string;
    quantity: string | null;
    uom: string | null;
    price_uom: string | null;
    unit_price_ten_thousandths: number | null;
    discount_percent: string | null;
    discount_cents: number | null;
    discount_reason: string | null;
    line_total_cents: number | null;
    taxable: boolean;
    unit_cost_ten_thousandths: number | null;
    cost_cents: number;
    restock?: boolean;
}

export interface LinesTableOptions {
    ariaLabel: string;
    /** A credit memo: quantities and extensions are negative and shown as credits. */
    credit?: boolean;
    /** Show the restock flag column (a credit memo's returned goods). */
    showRestock?: boolean;
}

/** A line's margin in cents, or null when it carries no cost (a note, a charge, a role without cost). */
export function lineMarginCents(line: Pick<SalesLineView, 'line_total_cents' | 'unit_cost_ten_thousandths' | 'cost_cents'>): number | null {
    if (line.line_total_cents === null || line.unit_cost_ten_thousandths === null) return null;
    return line.line_total_cents - line.cost_cents;
}

function discountText(line: SalesLineView): string {
    if (line.discount_percent !== null) return `${line.discount_percent}%`;
    if (line.discount_cents !== null) return formatCents(line.discount_cents);
    return '';
}

export function renderSalesLines(lines: SalesLineView[], opts: LinesTableOptions): TemplateResult {
    const ordered = [...lines].sort((a, b) => a.position - b.position);
    const showCost = ordered.some(l => l.unit_cost_ten_thousandths !== null);
    const credit = opts.credit === true;
    const cols = 6 + (opts.showRestock ? 1 : 0) + (showCost ? 2 : 0);
    return html`
        <div class="overflow-x-auto">
            <table class="w-full text-left text-sm" aria-label=${opts.ariaLabel}>
                <thead class="bg-white/5">
                    <tr>
                        <th class="p-3 text-muted-foreground font-medium">Item</th>
                        <th class="p-3 text-muted-foreground font-medium text-right">Qty</th>
                        <th class="p-3 text-muted-foreground font-medium text-right">Unit price</th>
                        <th class="p-3 text-muted-foreground font-medium text-right">Discount</th>
                        <th class="p-3 text-muted-foreground font-medium text-right">Total</th>
                        <th class="p-3 text-muted-foreground font-medium text-center">Tax</th>
                        ${opts.showRestock ? html`<th class="p-3 text-muted-foreground font-medium text-center">Restock</th>` : nothing}
                        ${showCost ? html`
                            <th class="p-3 text-muted-foreground font-medium text-right">Cost</th>
                            <th class="p-3 text-muted-foreground font-medium text-right">Margin</th>
                        ` : nothing}
                    </tr>
                </thead>
                <tbody class="divide-y divide-white/5">
                    ${ordered.map(line => {
                        if (line.line_type === 'text') {
                            return html`
                                <tr data-line-type="text">
                                    <td colspan=${cols} class="p-3 text-zinc-400 italic">${line.description}</td>
                                </tr>
                            `;
                        }
                        const isComponent = line.line_type === 'component' || line.parent_line_id !== null;
                        const margin = lineMarginCents(line);
                        return html`
                            <tr data-line-type=${line.line_type} class=${isComponent ? 'bg-black/20' : ''}>
                                <td class="p-3 text-white ${isComponent ? 'pl-10' : ''}">
                                    <div class="font-mono text-sm">
                                        ${line.sku || line.charge_code || line.line_type}
                                        <span class="ml-2 text-[10px] uppercase tracking-wide text-zinc-500">${line.line_type}</span>
                                    </div>
                                    <div class="text-xs text-muted-foreground">${line.description}</div>
                                </td>
                                <td class="p-3 font-mono text-right ${credit ? 'text-red-400' : 'text-white'}">
                                    ${formatQuantity(line.quantity)}
                                    ${line.uom ? html`<span class="text-xs text-zinc-500 ml-1">${line.uom}</span>` : nothing}
                                </td>
                                <td class="p-3 text-white font-mono text-right whitespace-nowrap">
                                    ${line.unit_price_ten_thousandths !== null ? formatPrice4(line.unit_price_ten_thousandths) : '—'}
                                    ${line.price_uom ? html`<span class="text-[10px] text-zinc-500 ml-1">/ ${line.price_uom}</span>` : nothing}
                                </td>
                                <td class="p-3 font-mono text-right text-blue-400 text-xs" title=${line.discount_reason ?? ''}>${discountText(line)}</td>
                                <td class="p-3 font-mono text-right font-medium ${credit ? creditTextClass(line.line_total_cents) : 'text-gable-green'}">
                                    ${line.line_total_cents !== null ? formatCents(line.line_total_cents) : '—'}
                                </td>
                                <td class="p-3 text-center text-xs ${line.taxable ? 'text-zinc-300' : 'text-zinc-600'}">${line.taxable ? 'T' : '-'}</td>
                                ${opts.showRestock ? html`<td class="p-3 text-center text-xs ${line.restock ? 'text-gable-green' : 'text-zinc-600'}">${line.restock ? 'Yes' : '-'}</td>` : nothing}
                                ${showCost ? html`
                                    <td class="p-3 font-mono text-right text-zinc-300">${line.unit_cost_ten_thousandths !== null ? formatCents(line.cost_cents) : '—'}</td>
                                    <td class="p-3 font-mono text-right ${margin !== null && margin < 0 && !credit ? 'text-red-400' : 'text-zinc-300'}">${margin !== null ? formatCents(margin) : '—'}</td>
                                ` : nothing}
                            </tr>
                        `;
                    })}
                </tbody>
            </table>
        </div>
    `;
}
