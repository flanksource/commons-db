import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("@flanksource/clicky-ui", async (importOriginal) => {
  const clicky = await importOriginal<typeof import("@flanksource/clicky-ui")>();

  return {
    ...clicky,
    FormatOptionsDropdown: ({ label }: { label: ReactNode }) => (
      <button aria-label="Choose format">{label}</button>
    ),
  };
});

import {
  ReconcileExport,
  reconcileExportOptions,
  reconcileExportURL,
} from "./reconcileExport";

describe("reconcile export", () => {
  const formats = ["json", "yaml", "csv", "markdown", "html", "pdf", "excel"];

  it("uses the formats advertised by the reconcile action and Clicky's native labels", () => {
    expect(reconcileExportOptions([...formats, "unknown"]).map(({ value, label }) => ({ value, label }))).toEqual([
      { value: "json", label: "JSON" },
      { value: "yaml", label: "YAML" },
      { value: "csv", label: "CSV" },
      { value: "markdown", label: "Markdown" },
      { value: "html", label: "HTML" },
      { value: "pdf", label: "PDF" },
      { value: "excel", label: "Excel" },
    ]);
  });

  it("preserves the exact reconcile window while selecting a server-rendered download", () => {
    const url = new URL(
      reconcileExportURL(
        "/api/v1/profiles/orders/reconcile?dest=warehouse&key-cel=row.id&limit=250",
        "csv",
        "orders to warehouse reconcile",
        "only_source",
        1234,
      ),
      "http://query.local",
    );

    expect(Object.fromEntries(url.searchParams)).toEqual({
      dest: "warehouse",
      "key-cel": "row.id",
      limit: "250",
      outcome: "only_source",
      format: "csv",
      filename: "orders-to-warehouse-reconcile.csv",
      _download: "1234",
    });
  });

  it("renders Clicky's native format picker", () => {
    const markup = renderToStaticMarkup(
      <ReconcileExport
        requestUrl="/api/v1/profiles/orders/reconcile?dest=warehouse"
        formats={formats}
        label="orders to warehouse reconcile"
        outcome="matched"
      />,
    );

    expect(markup).toContain("Export");
    expect(markup).toContain("Choose format");
  });
});
