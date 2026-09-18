// Tailwind for the facet diagrams only. Every utility is scoped under
// .facet-diagram and preflight is off, so none of it restyles Starlight.
// The theme mirrors @flanksource/facet's own tailwind.config.js, which the
// diagrams were designed against (point type scale, Open Sans / Fira Code).
export default {
  content: [
    "./src/diagrams/**/*.{ts,tsx}",
    "./node_modules/@flanksource/facet/dist/components/**/*.js",
  ],
  important: ".facet-diagram",
  corePlugins: { preflight: false },
  theme: {
    extend: {
      fontSize: {
        xs: ["7pt", { lineHeight: "9pt" }],
        sm: ["9pt", { lineHeight: "12pt" }],
        base: ["10pt", { lineHeight: "14pt" }],
        md: ["10pt", { lineHeight: "14pt" }],
        lg: ["15pt", { lineHeight: "19pt" }],
        xl: ["18pt", { lineHeight: "22pt" }],
        "2xl": ["24pt", { lineHeight: "28pt" }],
      },
      fontFamily: {
        sans: ["Open Sans", "system-ui", "-apple-system", "BlinkMacSystemFont", "Segoe UI", "Roboto", "sans-serif"],
        mono: ["Fira Code", "Consolas", "Monaco", "Courier New", "monospace"],
      },
    },
  },
};
