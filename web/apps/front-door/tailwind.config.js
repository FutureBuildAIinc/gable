// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The theme lives in @gable/design-system (the desk's tokens, moved not
// redrawn); this config keeps only the door's content globs. The package's
// sources are scanned so the shared components' classes generate here too.
import { gableTheme } from '@gable/design-system/tailwind';

/** @type {import('tailwindcss').Config} */
export default {
    darkMode: ["class"],
    content: [
        "./index.html",
        "./src/**/*.{ts,tsx,js,jsx}",
        "../../packages/design-system/src/**/*.{ts,tsx,js,jsx}",
    ],
    theme: gableTheme,
}
