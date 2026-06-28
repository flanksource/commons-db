if (import.meta.env.DEV) {
  import("react-grab").then(({ registerPlugin }) => {
    registerPlugin({
      name: "page-url",
      hooks: {
        transformCopyContent: (content: string) =>
          `${content}\n\nURL: ${window.location.href}`,
      },
    });
  });
}

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setFallbackIconProvider } from "@flanksource/clicky-ui";
import "@flanksource/clicky-ui/styles.css";
import { App } from "./App";
import { FlanksourceIcon } from "./iconProvider";

// Resolve clicky-ui's name-based icons (e.g. the connection-type picker grid)
// against @flanksource/icons.
setFallbackIconProvider(FlanksourceIcon);

const el = document.getElementById("root");
if (!el) {
  throw new Error("missing #root element");
}
createRoot(el).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
