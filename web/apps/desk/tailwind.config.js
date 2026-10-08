// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The theme itself lives in @gable/design-system (moved, not redrawn) so the
// front door and the desk render the same tokens. This config keeps only the
// desk's content globs; the package's sources are scanned too, because the
// shared components' Tailwind classes must be generated into this bundle.
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
