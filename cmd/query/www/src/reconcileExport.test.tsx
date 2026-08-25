import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  ReconcileProvenancePanel,
  ReconcileResultSettings,
  materializeParams,
  snapshotColumnNames,
} from "./reconcileExport";
import { virtualProfileHref, type ReconcileSnapshot } from "./reconcileModel";

const snapshot: ReconcileSnapshot = {
  id: "snapshot-1",
  connection: "connection://reconciliations/reconciliation-1",
  connection_id: "connection-1",
  profile: "reconciliations/one/results",
  surface: "profile-reconciliations-one-results",
  url: "/api/v1/profile/reconciliations%2Fone%2Fresults",
  columns: [
    { name: "key", label: "Key" },
    { name: "source,value", label: "Source value" },
    { name: "row_id", hidden: true },
  ],
  row_count: 12,
  stats: { matched: 8, only_source: 3, only_dest: 1, dup_keys: 0 },
  source: "orders",
  dest: "warehouse",
  idle_age: 3_600_000_000_000,
  expires_at: "2026-08-12T14:00:00Z",
};

describe("reconciliation snapshot export", () => {
  it("offers every visible snapshot column in stored order", () => {
    expect(snapshotColumnNames(snapshot)).toEqual(["key", "source,value"]);
  });

  it("encodes selected columns for the materialization sub-action", () => {
    expect(materializeParams(snapshot, snapshot.profile, ["key", "source,value"], "")).toEqual({
      snapshot: "snapshot-1",
      profile: "reconciliations/one/results",
      column: 'key,"source,value"',
    });
  });

  it("includes CEL when constructing transformed snapshot data", () => {
    expect(materializeParams(snapshot, snapshot.profile, ["key"], "dyn(rows)")).toMatchObject({
      cel: "dyn(rows)",
      column: "key",
    });
  });

  it("renders the Results panel with columns and the virtual profile", () => {
    const markup = renderToStaticMarkup(
      <ReconcileResultSettings
        active={snapshot}
        virtualProfileHref={virtualProfileHref(snapshot, { lane: "only_source", page: 0, pageSize: 100 })}
        selected={["key"]}
        onSelected={vi.fn()}
        cel=""
        onCEL={vi.fn()}
        onApply={vi.fn()}
        applying={false}
        error=""
      />,
    );

    expect(markup).toContain("Results");
    expect(markup).toContain("Source value");
    expect(markup).toContain("Open virtual profile");
  });

  it("shows the join and what each side actually ran", () => {
    const markup = renderToStaticMarkup(
      <ReconcileProvenancePanel
        provenance={{
          config: {
            dest: "warehouse",
            key: { cel: "row.order_id" },
            sourceFilters: { region: "eu" },
          },
          execution: {
            mode: "buffered",
            source: {
              side: "source",
              profile: "orders",
              provider: "postgres",
              query: "select * from orders where region = '{{.params.region}}'",
              diagnostics: {
                provider: "postgres",
                request: {
                  query: "SELECT * FROM orders WHERE region = $1 LIMIT 100",
                  rendered: "select * from orders where region = 'eu'",
                  connection: "connection://ops/warehouse",
                  arguments: ["eu"],
                },
                response: { returnedRows: 8134, durationMs: 412, pages: 3 },
              },
              rows: 8134,
              pages: 3,
              durationMs: 430,
              backendMs: 412,
            },
            dest: { side: "dest", profile: "warehouse", provider: "opensearch", rows: 8130, durationMs: 890 },
          },
        }}
      />,
    );

    expect(markup).toContain("Queries");
    expect(markup).toContain("row.order_id");
    expect(markup).toContain("region=eu");
    // The statement the backend was sent, and the template it came from.
    expect(markup).toContain("SELECT * FROM orders WHERE region = $1 LIMIT 100");
    expect(markup).toContain("select * from orders where region = &#x27;eu&#x27;");
    expect(markup).toContain("connection://ops/warehouse");
    expect(markup).toContain("8134");
    expect(markup).toContain("412ms");
  });
});
