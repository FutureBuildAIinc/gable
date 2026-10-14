// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's keyboard roster: which key does what, how a pressed key is
// spelled, and what the pinned legend shows. Pure: no DOM, no state.
//
// A function key types nothing, so it may stand alone. A letter or digit
// types, so it is a chord only with Ctrl or Alt; a bare or shifted letter is
// always typing and never a hotkey. A binding matches its exact chord, so any
// extra modifier falls through to the browser untouched.

export type HotkeyAction =
  | 'search' | 'customer' | 'quantity' | 'add' | 'next' | 'back'
  | 'tender-cash' | 'tender-check' | 'tender-card' | 'tender-account'
  | 'pay' | 'new-sale' | 'clear-sale' | 'void';

/** Every action the counter has, in the order the legend shows them. */
export const ACTIONS: readonly HotkeyAction[] = [
  'search', 'customer', 'quantity', 'add', 'next', 'back',
  'tender-cash', 'tender-check', 'tender-card', 'tender-account',
  'pay', 'new-sale', 'clear-sale', 'void',
];

export interface HotkeyBinding {
  action: HotkeyAction;
  /** The canonical chord: modifiers in the order shift, ctrl, alt, then the key, lowercase. */
  key: string;
  /** One or two words: the legend is chrome, not prose. */
  label: string;
}

/** One key per action. Function keys for the sale's flow, Alt chords for the rest. */
export const HOTKEYS: readonly HotkeyBinding[] = [
  { action: 'search', key: 'f2', label: 'Search' },
  { action: 'customer', key: 'f3', label: 'Customer' },
  { action: 'quantity', key: 'alt-q', label: 'Quantity' },
  { action: 'add', key: 'alt-a', label: 'Add' },
  { action: 'next', key: 'f4', label: 'Next' },
  { action: 'back', key: 'shift-f4', label: 'Back' },
  { action: 'tender-cash', key: 'f6', label: 'Cash' },
  { action: 'tender-check', key: 'f7', label: 'Check' },
  { action: 'tender-card', key: 'f8', label: 'Card' },
  { action: 'tender-account', key: 'f9', label: 'Account' },
  { action: 'pay', key: 'f12', label: 'Pay' },
  { action: 'new-sale', key: 'alt-n', label: 'New sale' },
  { action: 'clear-sale', key: 'alt-x', label: 'Clear sale' },
  { action: 'void', key: 'alt-z', label: 'Void' },
];

const FUNCTION_KEY = /^F([1-9]|1[0-2])$/;
const TYPED_KEY = /^[a-zA-Z0-9]$/;

/**
 * The canonical spelling of a pressed key, or '' when the press is not one a
 * hotkey may claim (ordinary typing, a named key, the meta key).
 */
export function chordFromEvent(e: { key: string; shiftKey?: boolean; ctrlKey?: boolean; altKey?: boolean; metaKey?: boolean }): string {
  if (e.metaKey) return '';
  const isFunction = FUNCTION_KEY.test(e.key);
  const isTyped = TYPED_KEY.test(e.key);
  if (!isFunction && !isTyped) return '';
  if (isTyped && !e.ctrlKey && !e.altKey) return '';
  const parts: string[] = [];
  if (e.shiftKey) parts.push('shift');
  if (e.ctrlKey) parts.push('ctrl');
  if (e.altKey) parts.push('alt');
  parts.push(e.key.toLowerCase());
  return parts.join('-');
}

/** The action a chord runs, or null: the caller then leaves the press alone. */
export function resolveHotkey(chord: string): HotkeyAction | null {
  if (!chord) return null;
  return HOTKEYS.find((h) => h.key === chord)?.action ?? null;
}

/** A chord as printed on a keycap: `shift-f4` reads `Shift+F4`. */
export function keycap(chord: string): string {
  return chord.split('-').map((p) => (p.length === 1 || FUNCTION_KEY.test(p.toUpperCase())
    ? p.toUpperCase()
    : p.charAt(0).toUpperCase() + p.slice(1))).join('+');
}

export interface LegendKey {
  action: HotkeyAction;
  key: string;
  label: string;
  keycap: string;
  live: boolean;
}

/**
 * The legend's keys in roster order. Every bound action shows at every
 * state: an unavailable one is dimmed, never removed, so the legend does not
 * move under the cashier's hand.
 */
export function legendKeys(isLive: (action: HotkeyAction) => boolean): LegendKey[] {
  return HOTKEYS.map((h) => ({ action: h.action, key: h.key, label: h.label, keycap: keycap(h.key), live: isLive(h.action) }));
}
