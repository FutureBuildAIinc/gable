// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A kit on a sales document is one KIT line with a COMPONENT line for every
 * part of the kit's definition beneath it (ADR 0005 sections 2.1 and 2.6). The
 * server writes those lines; the desk reads them and shows the parts indented
 * under the kit they belong to.
 *
 * The wire names are lowercase because the server marshals a line kind in
 * lower case (core/internal/salesdoc/lines.go), the same names the invoice
 * lines table already reads.
 */

import { extensionCents } from './money';
import type { QuoteLine } from '../types/quote';

/** The line kind of a kit line. */
export const KIT_LINE_TYPE = 'kit';

/** The line kind of a component line. */
export const COMPONENT_LINE_TYPE = 'component';

/** The line fields the grouping needs: every sales line carries them. */
export interface SalesLineLike {
    id: string;
    line_type?: string | null;
    parent_line_id?: string | null;
}

/** A top level line and the component lines the server wrote beneath it. */
export interface KitLineGroup<T extends SalesLineLike> {
    /** The line itself: a product line, a kit line, a charge line or a text line. */
    line: T;
    /** The parts of a kit line, empty for every other line. */
    components: T[];
}

/** True when the line is a kit the server exploded into parts. */
export function isKitLine(line: SalesLineLike): boolean {
    return line.line_type === KIT_LINE_TYPE;
}

/** True when the line is one part of a kit. */
export function isComponentLine(line: SalesLineLike): boolean {
    return line.line_type === COMPONENT_LINE_TYPE;
}

/**
 * Groups the lines of a sales document: every line that is not a component
 * stands on its own, and each component is filed under the kit line that names
 * it. A component whose parent is missing keeps its own place, so a document
 * never loses a line.
 */
export function groupKitLines<T extends SalesLineLike>(lines: T[]): KitLineGroup<T>[] {
    const byId = new Map<string, T>(lines.map(l => [l.id, l]));
    const parts = new Map<string, T[]>();
    const tops: T[] = [];
    for (const line of lines) {
        const parentId = isComponentLine(line) ? line.parent_line_id : null;
        if (parentId && byId.has(parentId)) {
            const siblings = parts.get(parentId);
            if (siblings) siblings.push(line);
            else parts.set(parentId, [line]);
        } else {
            tops.push(line);
        }
    }
    return tops.map(line => ({ line, components: parts.get(line.id) ?? [] }));
}

/**
 * The cost of a quote line in cents. A kit line carries no cost of its own:
 * the dealer carries the cost of its parts, and the server prices a kit's cost
 * from its component lines the same way (ADR 0005 section 2.6), so the desk
 * does the same to agree with it to the cent.
 */
export function lineCostCents(line: QuoteLine): number {
    if (isKitLine(line)) return 0;
    return extensionCents(line.quantity, line.unit_cost_ten_thousandths);
}

/**
 * The cost of a line with its parts, in cents. A kit's cost is its parts' cost.
 */
export function groupCostCents(group: KitLineGroup<QuoteLine>): number {
    return group.components.reduce((sum, c) => sum + lineCostCents(c), lineCostCents(group.line));
}
