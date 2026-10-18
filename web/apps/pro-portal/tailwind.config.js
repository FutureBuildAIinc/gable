// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { gableTheme } from '@gable/design-system/tailwind';

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ['class'],
  content: [
    './index.html',
    './customer.html',
    './src/**/*.{ts,tsx,js,jsx}',
    '../../packages/design-system/src/**/*.{ts,tsx,js,jsx}',
  ],
  theme: gableTheme,
}
