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
    expect(snapshotPageParams("only_source", 2, 50)).toEqual({
      "filter.outcome": "only_source",
      limit: "50",
      offset: "100",
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
