// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CatalogTiles } from '../../../services/POSService';

const svc = vi.hoisted(() => ({ tiles: vi.fn() }));

vi.mock('../../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../../services/POSService')>();
  return { ...real, posService: svc };
});

// The real scanner starts a camera; a plain element stands in for it.
vi.mock('../../../components/BarcodeScanner.ts', () => {
  if (!customElements.get('gable-barcode-scanner')) {
    customElements.define('gable-barcode-scanner', class extends HTMLElement {});
  }
  return {};
});

import './gable-pos-catalog';
import type { GablePosCatalog } from './gable-pos-catalog';
import { mount, update, q } from '../../../test/dom';

const item = (sku: string, over: Record<string, unknown> = {}) => ({
  product_id: `id-${sku}`, sku, description: `Product ${sku}`, uom: 'PCS', unit_price_cents: 550,
  in_stock: '514', category: 'Lumber', image_url: `/img/${sku}.svg`, ...over,
});
const FIXTURE: CatalogTiles = {
  categories: [{ name: 'Lumber', count: 3 }, { name: 'Pavers', count: 2 }],
  items: [item('LUM-248'), item('LUM-248-PREM'), item('PAV-60', { image_url: null, category: 'Pavers' })],
  images: true,
};

const tick = async (el: GablePosCatalog) => {
  await new Promise((r) => setTimeout(r, 0));
  await el.updateComplete;
};
const search = (el: GablePosCatalog) => q<HTMLInputElement>(el, '[data-testid="catalog-search"]');
const qty = (el: GablePosCatalog) => q<HTMLInputElement>(el, '[data-testid="catalog-qty"]');
const type = (el: GablePosCatalog, value: string) => {
  const input = search(el);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
};
const enter = (el: GablePosCatalog) =>
  search(el).dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));

let el: GablePosCatalog;
let added: Array<{ product: Record<string, unknown>; qty: string }>;

async function open(props: Partial<GablePosCatalog> = {}) {
  el = await mount<GablePosCatalog>('gable-pos-catalog', props);
  added = [];
  el.addEventListener('pos-catalog-add', (e) => added.push((e as CustomEvent).detail));
  await tick(el);
  return el;
}

beforeEach(() => {
  svc.tiles.mockReset();
  svc.tiles.mockResolvedValue(FIXTURE);
});
afterEach(() => {
  document.body.innerHTML = '';
  vi.useRealTimers();
});

describe('gable-pos-catalog', () => {
  it('reads the yard top sellers and renders the chips and tiles', async () => {
    await open();
    expect(svc.tiles).toHaveBeenCalledTimes(1);
    expect(svc.tiles).toHaveBeenCalledWith({ category: undefined, q: undefined });
    const chips = [...el.querySelectorAll('[data-testid="catalog-chip"]')].map((c) => c.textContent?.trim());
    expect(chips).toEqual(['All', 'Lumber', 'Pavers']);
    const tiles = el.querySelectorAll('[data-testid="catalog-tile"]');
    expect(tiles).toHaveLength(3);
    expect(tiles[1].textContent).toContain('LUM-248-PREM · 514 on hand');
    expect(tiles[1].textContent).toContain('$5.50 / PCS');
  });

  it('asks for the category when a chip is chosen', async () => {
    await open();
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[2].click();
    await tick(el);
    expect(svc.tiles).toHaveBeenLastCalledWith({ category: 'Pavers', q: undefined });
  });

  it('asks for q after two characters, debounced, across every category', async () => {
    await open();
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[1].click();
    await tick(el);
    svc.tiles.mockClear();
    type(el, 'l');
    await new Promise((r) => setTimeout(r, 400));
    expect(svc.tiles).not.toHaveBeenCalled();
    type(el, 'lu');
    expect(svc.tiles).not.toHaveBeenCalled();
    await new Promise((r) => setTimeout(r, 400));
    expect(svc.tiles).toHaveBeenCalledTimes(1);
    expect(svc.tiles).toHaveBeenCalledWith({ category: undefined, q: 'lu' });
  });

  it('adds the exact SKU on Enter with the typed quantity, then resets it to 1', async () => {
    await open();
    svc.tiles.mockResolvedValue({ ...FIXTURE, items: [FIXTURE.items[0], FIXTURE.items[1]] });
    qty(el).value = '12';
    qty(el).dispatchEvent(new Event('input', { bubbles: true }));
    type(el, 'LUM-248-PREM');
    enter(el);
    await new Promise((r) => setTimeout(r, 20));
    await el.updateComplete;
    expect(added).toHaveLength(1);
    expect(added[0].qty).toBe('12');
    expect(added[0].product).toEqual({
      product_id: 'id-LUM-248-PREM', sku: 'LUM-248-PREM', description: 'Product LUM-248-PREM', uom: 'PCS',
      unit_price_cents: 550, in_stock: '514', image_url: '/img/LUM-248-PREM.svg',
    });
    // the page has not accepted the add yet: what was typed stays
    expect(qty(el).value).toBe('12');
    el.resetQuantity();
    await el.updateComplete;
    expect(qty(el).value).toBe('1');
    expect(search(el).value).toBe('');
  });

  it('keeps the typed quantity after a tile add until the page resets it', async () => {
    await open();
    qty(el).value = '2.5';
    qty(el).dispatchEvent(new Event('input', { bubbles: true }));
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-tile-add"]')[0].click();
    await el.updateComplete;
    expect(added).toHaveLength(1);
    expect(qty(el).value).toBe('2.5');
  });

  it('clears the old yard tiles at once on a yard change and blocks adds until the new read answers', async () => {
    await open({ yardKey: 'main' });
    let answer!: (v: CatalogTiles) => void;
    svc.tiles.mockReturnValueOnce(new Promise((r) => { answer = r; }));
    await update(el, { yardKey: 'north' });
    expect(el.querySelectorAll('[data-testid="catalog-tile"]')).toHaveLength(0);
    expect(el.textContent).toContain('Loading products');
    el.querySelector<HTMLButtonElement>('[data-testid="catalog-tile-add"]')?.click();
    expect(added).toHaveLength(0);
    answer({ ...FIXTURE, items: [item('NORTH-1')] });
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
    expect(el.querySelectorAll('[data-testid="catalog-tile"]')).toHaveLength(1);
  });

  it('adds the first result when no SKU matches exactly', async () => {
    await open();
    type(el, 'lum');
    enter(el);
    await new Promise((r) => setTimeout(r, 20));
    expect(added).toHaveLength(1);
    expect(added[0].product.sku).toBe('LUM-248');
    expect(added[0].qty).toBe('1');
  });

  it('a tile Add emits the event with the typed quantity, bubbling and composed', async () => {
    await open();
    const seen: CustomEvent[] = [];
    document.body.addEventListener('pos-catalog-add', (e) => seen.push(e as CustomEvent));
    qty(el).value = '4';
    qty(el).dispatchEvent(new Event('input', { bubbles: true }));
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-tile-add"]')[2].click();
    await tick(el);
    expect(added).toHaveLength(1);
    expect(added[0].product.sku).toBe('PAV-60');
    expect(added[0].qty).toBe('4');
    expect(seen).toHaveLength(1);
    expect(seen[0].composed).toBe(true);
  });

  it('disabled blocks every add, says why and sends nothing', async () => {
    await open({ disabled: true });
    expect(el.textContent).toContain('Open the till to start a sale');
    const buttons = el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-tile-add"]');
    expect([...buttons].every((b) => b.disabled)).toBe(true);
    buttons[0].click();
    type(el, 'LUM-248');
    enter(el);
    q<HTMLButtonElement>(el, '[data-testid="catalog-add"]').click();
    await new Promise((r) => setTimeout(r, 20));
    expect(added).toHaveLength(0);
  });

  it('shows the no picture mark for a missing image and for one that fails', async () => {
    await open();
    const tiles = el.querySelectorAll('[data-testid="catalog-tile"]');
    expect(tiles[2].querySelector('img')).toBeNull();
    expect(tiles[2].querySelector('[data-testid="catalog-no-picture"]')).not.toBeNull();
    const img = tiles[0].querySelector('img')!;
    img.dispatchEvent(new Event('error'));
    await tick(el);
    const first = el.querySelectorAll('[data-testid="catalog-tile"]')[0];
    expect(first.querySelector('img')).toBeNull();
    expect(first.querySelector('[data-testid="catalog-no-picture"]')).not.toBeNull();
  });

  it('drops an older answer that lands after a newer one', async () => {
    await open();
    let slow!: (v: CatalogTiles) => void;
    svc.tiles.mockReturnValueOnce(new Promise((r) => { slow = r; }));
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[1].click();
    const newer = { ...FIXTURE, items: [item('NEW-1')] };
    svc.tiles.mockResolvedValueOnce(newer);
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[2].click();
    await tick(el);
    slow({ ...FIXTURE, items: [item('OLD-1')] });
    await tick(el);
    const skus = el.textContent ?? '';
    expect(skus).toContain('NEW-1');
    expect(skus).not.toContain('OLD-1');
  });

  it('re-reads when yardKey changes', async () => {
    await open({ yardKey: 'main' });
    svc.tiles.mockClear();
    await update(el, { yardKey: 'north' });
    await tick(el);
    expect(svc.tiles).toHaveBeenCalledTimes(1);
  });

  it('shows one quiet line with Retry when the read fails', async () => {
    svc.tiles.mockRejectedValueOnce(new Error('boom'));
    await open();
    expect(el.textContent).toContain('Products could not be loaded');
    q<HTMLButtonElement>(el, '[data-testid="catalog-retry"]').click();
    await tick(el);
    expect(el.textContent).not.toContain('Products could not be loaded');
    expect(el.querySelectorAll('[data-testid="catalog-tile"]')).toHaveLength(3);
  });

  it('adds nothing for fewer than two characters and says so', async () => {
    await open();
    type(el, 'l');
    enter(el);
    await new Promise((r) => setTimeout(r, 20));
    await el.updateComplete;
    expect(added).toHaveLength(0);
    expect(el.textContent).toContain('Type at least two characters');
    q<HTMLButtonElement>(el, '[data-testid="catalog-add"]').click();
    await new Promise((r) => setTimeout(r, 20));
    expect(added).toHaveLength(0);
  });

  it('searches every category while a search is typed and restores the chip when cleared', async () => {
    await open();
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[2].click();
    await tick(el);
    const pressed = () => [...el.querySelectorAll('[data-testid="catalog-chip"]')]
      .map((c) => c.getAttribute('aria-pressed'));
    expect(pressed()).toEqual(['false', 'false', 'true']);
    svc.tiles.mockClear();
    type(el, 'LUM-248');
    await new Promise((r) => setTimeout(r, 400));
    await el.updateComplete;
    expect(svc.tiles).toHaveBeenLastCalledWith({ category: undefined, q: 'LUM-248' });
    expect(pressed()).toEqual(['true', 'false', 'false']);
    type(el, '');
    await new Promise((r) => setTimeout(r, 400));
    await el.updateComplete;
    expect(svc.tiles).toHaveBeenLastCalledWith({ category: 'Pavers', q: undefined });
    expect(pressed()).toEqual(['false', 'false', 'true']);
  });

  it('finds an exact SKU from another category on Enter with a chip selected', async () => {
    await open();
    el.querySelectorAll<HTMLButtonElement>('[data-testid="catalog-chip"]')[2].click();
    await tick(el);
    svc.tiles.mockImplementation(async (query: { category?: string; q?: string }) => ({
      ...FIXTURE,
      items: query.category ? [] : [FIXTURE.items[1]],
    }));
    type(el, 'LUM-248-PREM');
    enter(el);
    await new Promise((r) => setTimeout(r, 20));
    expect(added).toHaveLength(1);
    expect(added[0].product.sku).toBe('LUM-248-PREM');
  });

  describe('scan', () => {
    const scan = (code: string) => {
      q<HTMLButtonElement>(el, '[data-testid="catalog-scan"]').click();
      return el.updateComplete.then(() => {
        const scanner = q<HTMLElement>(el, 'gable-barcode-scanner');
        scanner.dispatchEvent(new CustomEvent('scan', { detail: code, bubbles: true, composed: true }));
      });
    };

    it('adds the exact match of a scanned code with the typed quantity', async () => {
      await open();
      svc.tiles.mockResolvedValue({ ...FIXTURE, items: [FIXTURE.items[1]] });
      qty(el).value = '3';
      qty(el).dispatchEvent(new Event('input', { bubbles: true }));
      await scan('LUM-248-PREM');
      await new Promise((r) => setTimeout(r, 20));
      await el.updateComplete;
      expect(svc.tiles).toHaveBeenLastCalledWith({ category: undefined, q: 'LUM-248-PREM' });
      expect(added).toHaveLength(1);
      expect(added[0].product.sku).toBe('LUM-248-PREM');
      expect(added[0].qty).toBe('3');
      expect(el.querySelector('gable-barcode-scanner')).toBeNull();
    });

    it('says a scanned code was not found and adds nothing', async () => {
      await open();
      svc.tiles.mockResolvedValue({ ...FIXTURE, items: [FIXTURE.items[0]] });
      await scan('9999999');
      await new Promise((r) => setTimeout(r, 20));
      await el.updateComplete;
      expect(added).toHaveLength(0);
      expect(el.textContent).toContain('Product not found for barcode: 9999999');
    });

    it('does not offer the scan while disabled', async () => {
      await open({ disabled: true });
      expect(q<HTMLButtonElement>(el, '[data-testid="catalog-scan"]').disabled).toBe(true);
    });
  });

  it('draws the picture at 72 pixels on a light plate and keeps unselected chips free of green', async () => {
    await open();
    const plate = el.querySelector('[data-testid="catalog-picture"]')!;
    expect(plate.className).toContain('h-[72px]');
    expect(plate.className).toContain('bg-white');
    const chip = el.querySelectorAll('[data-testid="catalog-chip"]')[2];
    expect(chip.className).not.toMatch(/gable-green/);
  });

  it('lays the grid out by its own width and keeps the chips on one row', async () => {
    await open();
    const grid = q<HTMLElement>(el, '[data-testid="catalog-grid"]');
    expect(grid.className).toContain('repeat(auto-fill,minmax(12.5rem,1fr))');
    expect(grid.className).not.toMatch(/\b(lg|xl):grid-cols/);
    const row = q<HTMLElement>(el, '[data-testid="catalog-chips"]');
    expect(row.className).toContain('flex-nowrap');
    expect(row.className).toContain('overflow-x-auto');
  });
});
