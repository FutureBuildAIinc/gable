// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The theme lives in @gable/design-system; this config keeps only the shell's
// content globs. The shared packages' sources are scanned so the chrome
// elements' classes generate here too.
import { gableTheme } from '@gable/design-system/tailwind';

/** @type {import('tailwindcss').Config} */
export default {
    darkMode: ["class"],
    content: [
        "./index.html",
        "./src/**/*.{ts,tsx,js,jsx}",
        "../../packages/design-system/src/**/*.{ts,tsx,js,jsx}",
        "../../packages/shell-chrome/src/**/*.{ts,tsx,js,jsx}",
    ],
    theme: gableTheme,
}
