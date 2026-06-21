import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@flanksource/clicky-ui/styles.css";
import { App } from "./App";

const el = document.getElementById("root");
if (!el) {
  throw new Error("missing #root element");
}
createRoot(el).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
