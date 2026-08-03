import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { setFallbackIconProvider } from "@flanksource/clicky-ui";
import "@flanksource/clicky-ui/styles.css";
import "../src/styles.css";
import "./design.css";
import { FlanksourceIcon } from "../src/iconProvider";
import { VariantA } from "./variantA";
import { VariantB } from "./variantB";
import { VariantC } from "./variantC";
import { VariantD } from "./variantD";

setFallbackIconProvider(FlanksourceIcon);

type VariantKey = "a" | "b" | "c" | "d";

const variants: { key: VariantKey; name: string; shell: string; pitch: string }[] = [
  {
    key: "a",
    name: "A · Two-pane modal",
    shell: "Modal · top tabs",
    pitch: "The layout the code already intends, made to render. List and inspector side by side, each scrolling independently.",
  },
  {
    key: "b",
    name: "B · Right drawer",
    shell: "Drawer · section rail",
    pitch: "Keeps the profile table visible while you edit. Vertical rail gives each section a hint line and a state chip.",
  },
  {
    key: "c",
    name: "C · Full page",
    shell: "Route · Workspace panes",
    pitch: "clicky-ui Workspace owns the panes — resizable, collapsible, layout persisted. Fields are an editable grid; inspector only for CEL.",
  },
  {
    key: "d",
    name: "D · Workspace modal",
    shell: "Modal · 3 panes + dock",
    pitch: "Rail, list and inspector in one overlay, plus a live preview dock so you see the table you are configuring.",
  },
];

function Playground() {
  const [variant, setVariant] = useState<VariantKey>("a");
  const [open, setOpen] = useState(true);
  const active = variants.find((item) => item.key === variant)!;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const index = Number(event.key) - 1;
      if (index >= 0 && index < variants.length) {
        setVariant(variants[index].key);
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const show = (key: VariantKey) => {
    setVariant(key);
    setOpen(true);
  };

  return (
    <>
      <header
        className="fixed inset-x-0 top-0 flex h-10 items-center gap-2 border-b border-border bg-background px-3"
        style={{ zIndex: 2147483000 }}
      >
        <span className="text-xs font-semibold">Profile editor · design options</span>
        <div className="flex items-center gap-1">
          {variants.map((item, index) => (
            <button
              key={item.key}
              type="button"
              title={item.pitch}
              className={`rounded-md px-2.5 py-1 text-xs transition-colors ${
                item.key === variant
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground"
              }`}
              onClick={() => show(item.key)}
            >
              <span className="mr-1 opacity-60">{index + 1}</span>
              {item.name}
            </button>
          ))}
        </div>
        <span className="ml-auto truncate text-[11px] text-muted-foreground">{active.shell}</span>
        {!open ? (
          <button
            type="button"
            className="rounded-md bg-primary px-2.5 py-1 text-xs text-primary-foreground"
            onClick={() => setOpen(true)}
          >
            Reopen
          </button>
        ) : null}
      </header>

      <div className="fixed inset-x-0 bottom-0 top-10 overflow-hidden">
        {open && variant === "c" ? <VariantC onClose={() => setOpen(false)} /> : null}
        {!open ? (
          <div className="grid h-full place-items-center px-6 text-center">
            <div className="max-w-lg">
              <p className="text-sm font-medium">{active.name} closed</p>
              <p className="mt-1 text-xs text-muted-foreground">{active.pitch}</p>
              <p className="mt-3 text-[11px] text-muted-foreground">
                Press 1–4 to switch options, or Reopen.
              </p>
            </div>
          </div>
        ) : null}
      </div>

      {open && variant === "a" ? <VariantA onClose={() => setOpen(false)} /> : null}
      {open && variant === "b" ? <VariantB onClose={() => setOpen(false)} /> : null}
      {open && variant === "d" ? <VariantD onClose={() => setOpen(false)} /> : null}
    </>
  );
}

const el = document.getElementById("root");
if (!el) throw new Error("missing #root element");
createRoot(el).render(
  <StrictMode>
    <Playground />
  </StrictMode>,
);
