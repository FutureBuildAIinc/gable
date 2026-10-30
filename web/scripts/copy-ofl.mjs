// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Post-build step: the OFL text for the bundled Outfit and JetBrains Mono
// font files travels with every web and Tauri bundle, next to the fonts.
// Run from an app directory: node ../../scripts/copy-ofl.mjs
import { copyFileSync, existsSync } from 'node:fs'

const licence = new URL('../../LICENSES/OFL-1.1.txt', import.meta.url)
if (!existsSync('dist')) throw new Error('copy-ofl: no dist/ in ' + process.cwd())
copyFileSync(licence, 'dist/OFL-1.1.txt')
