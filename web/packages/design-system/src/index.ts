// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * @gable/design-system — Gable's shared tokens and the components every
 * frontend bundle renders (the desk and the front door). The Tailwind theme
 * lives at '@gable/design-system/tailwind' and the CSS variables at
 * './tokens.css'; both apps import them so nothing is redrawn.
 *
 * Importing this module registers the shared custom elements (the gable-
 * prefix rule holds across packages).
 */

export { cn } from './cn.ts';
export { icon } from './icons.ts';
export { GableBrandLogo } from './brand-logo.ts';
export { GableThemeToggle } from './theme-toggle.ts';
export {
  FRAMED_PARAM,
  FRAMED_BY_WORK_SHELL,
  CHROME_ACTION_MESSAGE,
  CHROME_CONTROLS_MESSAGE,
  CHROME_TITLE_MESSAGE,
  FRAME_ACTION_MESSAGE,
  YARD_CHANGED_MESSAGE,
  SIGN_OUT_REQUEST_MESSAGE,
  SIGNED_OUT_MESSAGE,
  MAX_SIGN_OUT_NONCE_LENGTH,
  framedFlagFromSearch,
  readFramedByShell,
  framedByShell,
  framedAddress,
  chromeActionFrom,
  announceChromeControls,
  acceptChromeControls,
  announceChromeTitle,
  acceptChromeTitle,
  announceFrameAction,
  frameActionFrom,
  announceYardChanged,
  yardChangedFrom,
  SWITCH_ROLE_MESSAGE,
  announceChromeAction,
  announceSwitchRole,
  acceptSwitchRole,
  isFramedWindow,
  orphanedFrame,
  announceSignOut,
  announceSignedOut,
  signOutRequestFrom,
  signedOutFrom,
  listenForHostSignOut,
  type ChromeAction,
  type ChromeTitleFeed,
  type FrameAction,
  type FramedWindow,
  type FramedListenWindow,
} from './framed.ts';
export {
  currentTheme,
  setTheme,
  toggleTheme,
  initTheme,
  useThemeSubject,
  onThemeChange,
  themeColor,
  themeKeyFor,
  THEME_KEY,
  THEME_EVENT,
  type Theme,
} from './theme.ts';
