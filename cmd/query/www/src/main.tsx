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
import { clickyIconProvider } from "@flanksource/clicky-ui/icons";
import {
  configureProfiles,
  type ProfileSchema,
} from "@flanksource/clicky-ui/profiles";
import profileSchemaDocument from "../../../../schemas/profile.json";
import "./styles.css";
import "@flanksource/clicky-ui/styles.css";
import { App } from "./App";
import { FlanksourceIcon } from "./iconProvider";

// The profile editor ships in clicky-ui, but the schema it renders is generated
// from this repo's Go types — so we hand it ours rather than let the library
// carry a copy that would drift from what the server validates against. The
// mount point is the default /api/v1, so basePath is left alone.
configureProfiles({
  schema: profileSchemaDocument as unknown as ProfileSchema,
});

// Resolve clicky-ui's name-based icons (e.g. the connection-type picker grid).
// clicky-ui's own generated set resolves first (schema glyphs like the param
// type/role icons), with @flanksource/icons behind it for product and vendor
// marks it does not carry.
setFallbackIconProvider(clickyIconProvider(FlanksourceIcon));

const el = document.getElementById("root");
if (!el) {
  throw new Error("missing #root element");
}
createRoot(el).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
