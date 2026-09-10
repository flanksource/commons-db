import { describe, expect, it, vi } from "vitest";

import {
  prepareSnapshotDownload,
  snapshotPageParams,
} from "./reconcileResults";
import type { ReconcileSnapshot } from "./reconcileModel";

const snapshot = {
  id: "root",
  profile: "reconciliations/root/results",
  url: "/api/v1/profile/root",
  source: "a",
  dest: "b",
  columns: [{ name: "key" }, { name: "outcome" }],
} as ReconcileSnapshot;

describe("native reconciliation results", () => {
  it("pages and filters the generated profile on the server", () => {
    expect(snapshotPageParams({ lane: "only_source", page: 2, pageSize: 50 })).toEqual({
      "filter.outcome": "only_source",
      limit: "50",
      offset: "100",
    });
  });

  // The snapshot spans pages, so an order that stayed in the browser would sort
  // the hundred rows on screen and leave the rest where they were.
  it("sends a requested order to the server alongside the page", () => {
    expect(
      snapshotPageParams({ lane: "matched", page: 0, pageSize: 100, sort: "time_diff", desc: true }),
    ).toEqual({
      "filter.outcome": "matched",
      limit: "100",
      offset: "0",
      sort: "time_diff",
      order: "desc",
    });
  });

  it("constructs a projected profile before returning its native download URL", async () => {
    const materialize = vi.fn().mockResolvedValue({
      ...snapshot,
      profile: "reconciliations/root/results/materialized-columns",
      url: "/api/v1/profile/projected",
    });

    await expect(
      prepareSnapshotDownload(materialize, snapshot, snapshot, ["outcome", "key"]),
    ).resolves.toEqual({ url: "/api/v1/profile/projected", label: "a to b reconciliation" });
    expect(materialize).toHaveBeenCalledWith(snapshot, snapshot.profile, ["outcome", "key"], "");
  });
});
