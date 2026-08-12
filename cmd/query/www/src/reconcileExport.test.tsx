import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import {
  ReconcileResultSettings,
  materializeParams,
  snapshotColumnNames,
} from "./reconcileExport";
import type { ReconcileSnapshot } from "./reconcileModel";

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
});
