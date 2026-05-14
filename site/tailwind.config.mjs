/** @type {import('tailwindcss').Config} */
export default {
  content: ["./src/**/*.{astro,html,js,jsx,md,mdx,svelte,ts,tsx,vue}"],
  theme: {
    extend: {
      fontFamily: {
        sans: ['"Inter"', "system-ui", "sans-serif"],
        mono: ['"JetBrains Mono"', "ui-monospace", "SFMono-Regular", "monospace"],
        display: ['"Space Grotesk"', '"Inter"', "system-ui", "sans-serif"],
      },
      colors: {
        ink: {
          950: "#0a0a0c",
          900: "#0f1014",
          800: "#16181f",
          700: "#1f2230",
          600: "#2a2e40",
          500: "#3a3f55",
          400: "#5a607a",
          300: "#8b91a8",
          200: "#b4b9cc",
          100: "#dcdeea",
          50: "#f4f5fa",
        },
        verdict: {
          approve: "#34d399",
          warn: "#f59e0b",
          critical: "#f87171",
          adversary: "#a78bfa",
        },
      },
      typography: {
        DEFAULT: {
          css: {
            "code::before": { content: '""' },
            "code::after": { content: '""' },
          },
        },
      },
    },
  },
  plugins: [],
};
