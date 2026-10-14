// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Gable's Tailwind theme, MOVED verbatim from web/apps/desk/tailwind.config.js
 * when web/ became one workspace. Every app bundle (the desk, the front door)
 * spreads this into its own tailwind.config.js so the tokens have exactly one
 * home; the app's config keeps only what is app-specific (content globs).
 *
 * The raw colour values behind the aliases live in src/tokens.css as RGB
 * channel variables (--gb-*): Industrial Dark on :root, Gable light on
 * :root[data-theme="light"]. Every colour here reads them, so a utility such
 * as text-white, bg-white/5 or border-zinc-800 follows the mode.
 */

const STEPS = ['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'];
const PALETTES = [
    'zinc', 'slate', 'gray',
    'emerald', 'green', 'red', 'rose', 'amber', 'yellow', 'orange', 'blue',
    'sky', 'teal', 'indigo', 'violet', 'purple', 'pink', 'cyan', 'lime',
];

/** rgb(var(--gb-<name>) / <alpha-value>): one colour that follows the mode. */
const channel = (name) => `rgb(var(--gb-${name}) / <alpha-value>)`;

/** A full 50 to 950 palette read from the variables. */
const palette = (name) =>
    Object.fromEntries(STEPS.map((step) => [step, channel(`${name}-${step}`)]));

/** @type {import('tailwindcss').Config} */
export const gableTheme = {
    extend: {
        fontFamily: {
            sans: ['Outfit', 'sans-serif'],
            mono: ['JetBrains Mono', 'monospace'],
        },
        colors: {
            border: "hsl(var(--border))",
            input: "hsl(var(--input))",
            ring: "hsl(var(--ring))",
            background: "hsl(var(--background))",
            foreground: "hsl(var(--foreground))",

            // Material Design 3 Contextual Colors
            primary: {
                DEFAULT: "hsl(var(--primary))",
                foreground: "hsl(var(--primary-foreground))",
                container: "hsl(var(--primary-container))",
                "on-container": "hsl(var(--on-primary-container))",
            },
            secondary: {
                DEFAULT: "hsl(var(--secondary))",
                foreground: "hsl(var(--secondary-foreground))",
                container: "hsl(var(--secondary-container))",
                "on-container": "hsl(var(--on-secondary-container))",
            },
            tertiary: {
                DEFAULT: "hsl(var(--tertiary))",
                foreground: "hsl(var(--tertiary-foreground))",
            },
            destructive: {
                DEFAULT: "hsl(var(--destructive))",
                foreground: "hsl(var(--destructive-foreground))",
            },
            muted: {
                DEFAULT: "hsl(var(--muted))",
                foreground: "hsl(var(--muted-foreground))",
            },
            accent: {
                DEFAULT: "hsl(var(--accent))",
                foreground: "hsl(var(--accent-foreground))",
            },
            popover: {
                DEFAULT: "hsl(var(--popover))",
                foreground: "hsl(var(--popover-foreground))",
            },
            card: {
                DEFAULT: "hsl(var(--card))",
                foreground: "hsl(var(--card-foreground))",
            },

            // Specific Brand Aliases
            'gable-green': channel('gable-green'),
            'gable-green-hover': channel('gable-green-hover'),
            'gable-green-tint': channel('gable-green-tint'),
            'deep-space': channel('deep-space'), // Surface 0 (the ground)
            'slate-steel': channel('slate-steel'), // Surface 1
            'surface-2': channel('surface-2'),   // Surface 2 (Cards)
            'surface-3': channel('surface-3'),   // Surface 3 (Modals)
            'safety-red': channel('safety-red'),
            'blueprint-blue': channel('blueprint-blue'),
            // The second voice (tags, secondary chips)
            sage: channel('sage'),
            'sage-fill': channel('sage-fill'),
            'sage-tint': channel('sage-tint'),
            // Colours with their own dark value, so Industrial Dark draws exactly as before
            'pale': channel('pale'),
            // The second voice: dark draws today's neutrals, light draws sage
            'voice': channel('voice'),
            'voice-soft': channel('voice-soft'),
            'voice-text': channel('voice-text'),
            'voice-fill': channel('voice-fill'),
            'well': channel('well'),
            'well-hover': channel('well-hover'),
            'till-surface': channel('till-surface'),
            'till-edge': channel('till-edge'),
            'till-text': channel('till-text'),
            'till-muted': channel('till-muted'),
            'till-soft': channel('till-soft'),
            'till-field': channel('till-field'),
            'till-accent': channel('till-accent'),
            'till-go': channel('till-go'),
            'till-ok': channel('till-ok'),
            'till-bad': channel('till-bad'),
            'till-warn': channel('till-warn'),
            'till-rule': channel('till-rule'),
            // A picture plate: white as today, the modal value in light
            plate: channel('plate'),
            // A modal veil: black as today, the ink value in light
            backdrop: channel('backdrop'),

            // The neutral and status palettes follow the mode
            white: channel('white'),
            black: channel('black'),
            ...Object.fromEntries(PALETTES.map((name) => [name, palette(name)])),
        },
        borderRadius: {
            lg: 'var(--radius)',
            md: 'calc(var(--radius) - 2px)',
            sm: 'calc(var(--radius) - 4px)',
            'xl': '1.5rem', // MD3 Large (24dp)
            '2xl': '2rem',  // MD3 Extra Large (32dp)
        },
        boxShadow: {
            'elevation-1': 'var(--gb-elevation-1)',
            'elevation-2': 'var(--gb-elevation-2)',
            'elevation-3': 'var(--gb-elevation-3)',
            'glow': 'var(--gb-glow)',
            'glow-soft': 'var(--gb-glow-soft)',
            'glow-10-30': 'var(--gb-glow-10-30)',
            'glow-20-10': 'var(--gb-glow-20-10)',
            'glow-20-30': 'var(--gb-glow-20-30)',
            'glow-30-15': 'var(--gb-glow-30-15)',
            'glow-40-30': 'var(--gb-glow-40-30)',

            'glow-strong': 'var(--gb-glow-strong)',
        },
        animation: {
            'fade-in': 'fadeIn 0.3s cubic-bezier(0.2, 0.0, 0, 1.0)', // Emphasized easing
            'slide-up': 'slideUp 0.4s cubic-bezier(0.2, 0.0, 0, 1.0)',
            'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
            'shimmer': 'shimmer 2s linear infinite',
        },
        keyframes: {
            fadeIn: {
                '0%': { opacity: '0' },
                '100%': { opacity: '1' },
            },
            slideUp: {
                '0%': { transform: 'translateY(10px)', opacity: '0' },
                '100%': { transform: 'translateY(0)', opacity: '1' },
            },
            shimmer: {
                '0%': { backgroundPosition: '-1000px 0' },
                '100%': { backgroundPosition: '1000px 0' }
            }
        }
    },
};
