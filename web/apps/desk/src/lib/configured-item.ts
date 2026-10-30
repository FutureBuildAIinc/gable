// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The handoff of a configured item from the configurator to the document
 * that carries it.
 *
 * A configured item is priced by the server: the configurator sends the
 * choices, the server answers with the SKU, the description, the product row
 * and the money, and the item travels on as those answers. The price on the
 * line is the server's price, never the dealer's.
 *
 * The configurator and the Quote Builder are two routes, so the item crosses
 * between them through this module: the configurator offers it and the Quote
 * Builder takes it. Taking clears it, so an item is offered once and added
 * once.
 */

import type { ConfiguredConfiguration } from '../types/configurator';

/** A configured item, ready to be added to a quote or an order. */
export interface ConfiguredItem {
    /** The deterministic NS- SKU. */
    sku: string;
    /** The short description the documents carry. */
    description: string;
    /** The product row the configured item became. */
    product_id: string;
    /** The server's price, in ten-thousandths of the major unit. */
    unit_price_ten_thousandths: number;
    /** The server's cost, in ten-thousandths of the major unit. */
    unit_cost_ten_thousandths: number;
    /** The canonical configuration the price came from. */
    configuration_json: ConfiguredConfiguration;
}

let pending: ConfiguredItem | null = null;
const listeners = new Set<(item: ConfiguredItem) => void>();

export const ConfiguredItemHandoff = {
    /**
     * The item on offer, without taking it. A page that only wants to know
     * whether one is waiting reads this.
     */
    peek(): ConfiguredItem | null {
        return pending;
    },

    /** Takes the item on offer and clears it. */
    take(): ConfiguredItem | null {
        const item = pending;
        pending = null;
        return item;
    },

    /** Offers the item and tells the listeners. */
    offer(item: ConfiguredItem): void {
        pending = item;
        listeners.forEach((fn) => fn(item));
    },

    /** Subscribes to the offer; the returned function unsubscribes. */
    subscribe(fn: (item: ConfiguredItem) => void): () => void {
        listeners.add(fn);
        return () => {
            listeners.delete(fn);
        };
    },
};

/** The event a configurator fires when the dealer adds a configured item. */
export const CONFIGURED_ITEM_EVENT = 'configured-item';

/**
 * The event detail a configurator fires: the configured item, ready to be
 * added.
 */
export type ConfiguredItemDetail = ConfiguredItem;
