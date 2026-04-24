/** @type {import('tailwindcss').Config} */

const sizes = {
  '2xs': 0.5,
  xs: 0.75,
  sm: 0.875,
  base: 1,
  lg: 1.125,
  xl: 1.25,
}

const lineHeightMultiplier = 1.5
const radiusMultiplier = 0.25
const iconMultiplier = 1.25

function getSize(sizeLabel, multiplier = 1) {
  return sizes[sizeLabel] * multiplier + 'rem'
}

export default {
  darkMode: ['class'],
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        'ibm-plex-sans': ['"IBM Plex Sans"', '"Noto Emoji"', 'sans-serif'],
        'ibm-plex-mono': ['"IBM Plex Mono"', 'monospace'],
      },
      colors: {
        // Text hierarchy
        high: 'hsl(var(--text-high))',
        normal: 'hsl(var(--text-normal))',
        low: 'hsl(var(--text-low))',
        // Backgrounds
        primary: 'hsl(var(--bg-primary))',
        secondary: 'hsl(var(--bg-secondary))',
        panel: 'hsl(var(--bg-panel))',
        // Brand (orange, matching vibe-kanban)
        brand: 'hsl(var(--brand))',
        'brand-hover': 'hsl(var(--brand-hover))',
        'brand-secondary': 'hsl(var(--brand-secondary))',
        'on-brand': 'hsl(var(--text-on-brand))',
        // Status
        error: 'hsl(var(--error))',
        success: 'hsl(var(--success))',
        merged: 'hsl(var(--merged))',
        // shadcn compat aliases
        background: 'hsl(var(--bg-primary))',
        foreground: 'hsl(var(--text-normal))',
        border: 'hsl(var(--border))',
        muted: {
          DEFAULT: 'hsl(var(--_muted))',
          foreground: 'hsl(var(--_muted-foreground))',
        },
        destructive: {
          DEFAULT: 'hsl(var(--error))',
          foreground: 'hsl(var(--_destructive-foreground))',
        },
        card: {
          DEFAULT: 'hsl(var(--bg-secondary))',
          foreground: 'hsl(var(--text-normal))',
        },
        input: 'hsl(var(--_input))',
        ring: 'hsl(var(--brand))',
      },
      borderColor: {
        DEFAULT: 'hsl(var(--border))',
      },
      borderRadius: {
        lg: getSize('lg', radiusMultiplier),
        md: getSize('sm', radiusMultiplier),
        sm: getSize('xs', radiusMultiplier),
        xl: '0.5rem',
        '2xl': '0.75rem',
        full: '9999px',
      },
      spacing: {
        half: getSize('base', 0.25),
        base: getSize('base', 0.5),
        plusfifty: getSize('base', 0.75),
        double: getSize('base', 1),
      },
      size: {
        'icon-2xs': getSize('2xs', iconMultiplier),
        'icon-xs': getSize('xs', iconMultiplier),
        'icon-sm': getSize('sm', iconMultiplier),
        'icon-base': getSize('base', iconMultiplier),
        'icon-lg': getSize('lg', iconMultiplier),
        'icon-xl': getSize('xl', iconMultiplier),
      },
      keyframes: {
        'accordion-down': { from: { height: '0' }, to: { height: 'var(--radix-accordion-content-height)' } },
        'accordion-up': { from: { height: 'var(--radix-accordion-content-height)' }, to: { height: '0' } },
        'border-flash': { '0%': { backgroundPosition: '-200% 0' }, '100%': { backgroundPosition: '200% 0' } },
        'running-dot': { '0%, 100%': { opacity: '0.3' }, '50%': { opacity: '1' } },
        shake: { '0%, 100%': { transform: 'translateX(0)' }, '10%, 30%, 50%, 70%, 90%': { transform: 'translateX(-2px)' }, '20%, 40%, 60%, 80%': { transform: 'translateX(2px)' } },
        'fade-up': { from: { opacity: '0', transform: 'translateY(16px)' }, to: { opacity: '1', transform: 'translateY(0)' } },
      },
      animation: {
        'accordion-down': 'accordion-down 0.2s ease-out',
        'accordion-up': 'accordion-up 0.2s ease-out',
        'border-flash': 'border-flash 2s linear infinite',
        'running-dot-1': 'running-dot 1.4s ease-in-out infinite',
        'running-dot-2': 'running-dot 1.4s ease-in-out 0.2s infinite',
        'running-dot-3': 'running-dot 1.4s ease-in-out 0.4s infinite',
        shake: 'shake 0.3s ease-in-out',
        'fade-up': 'fade-up 0.5s ease-out both',
      },
    },
  },
  plugins: [require('tailwindcss-animate')],
}
