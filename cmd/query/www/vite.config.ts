import path from "node:path";
import { defineConfig, type ConfigEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The Go server (query serve) runs on :8080 by default. In `vite` dev mode the
// API is proxied there; in production the built dist/ is embedded and served by
// the Go binary, so requests are same-origin.
const apiTarget = process.env.QUERY_API_URL || "http://localhost:8080";

// `query serve --dev` runs Vite's serve command, which resolves clicky-ui from
// the sibling source checkout for hot reload. Production builds keep resolving
// the published package pinned in package.json.
const clickyUI = path.resolve(__dirname, "../../../../clicky-ui/packages/ui");
const clickyUISource = path.resolve(clickyUI, "src");

export function clickyUIDevAliases(command: ConfigEnv["command"]) {
  if (command !== "serve") return [];
  return [
    {
      find: "@flanksource/clicky-ui/styles.css",
      replacement: path.resolve(clickyUISource, "styles/full.css"),
    },
    {
      find: "@flanksource/clicky-ui/monaco/schema",
      replacement: path.resolve(clickyUISource, "monaco-schema.ts"),
    },
    ...["ai", "chat", "clicky", "components", "data", "hooks", "icons", "jotai", "mdx-editor", "monaco", "profiles", "rpc", "utils"].map((entrypoint) => ({
      find: `@flanksource/clicky-ui/${entrypoint}`,
      replacement: path.resolve(clickyUISource, `${entrypoint}.ts`),
    })),
    {
      find: "@flanksource/clicky-ui",
      replacement: path.resolve(clickyUISource, "index.ts"),
    },
  ];
}

export default defineConfig(({ command }) => ({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: clickyUIDevAliases(command),
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
}));
