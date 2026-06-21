import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go server (query serve) runs on :8080 by default. In `vite` dev mode the
// API is proxied there; in production the built dist/ is embedded and served by
// the Go binary, so requests are same-origin.
const apiTarget = process.env.QUERY_API_URL || "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
      "/health": { target: apiTarget, changeOrigin: true },
    },
  },
});
