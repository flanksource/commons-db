import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go server (query serve) runs on :8080 by default. In `vite` dev mode the
// API is proxied there; in production the built dist/ is embedded and served by
// the Go binary, so requests are same-origin.
const apiTarget = process.env.QUERY_API_URL || "http://localhost:8080";

// When @flanksource/clicky-ui is linked from the sibling checkout (pnpm
// workspace), it resolves to a symlink outside this project root — allow Vite to
// read it, and dedupe React so the linked package shares this app's single copy.
const clickyUI = path.resolve(__dirname, "../../../../clicky-ui/packages/ui");

export default defineConfig({
  plugins: [react()],
  resolve: {
    dedupe: ["react", "react-dom", "react/jsx-runtime", "@tanstack/react-query", "monaco-editor"],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    fs: {
      allow: [__dirname, clickyUI],
    },
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
      "/health": { target: apiTarget, changeOrigin: true },
    },
  },
});
