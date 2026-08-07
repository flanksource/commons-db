import { describe, expect, it } from "vitest";
import { mergeProviderOptions } from "./connectionBrowserModel";

describe("provider option layering", () => {
  const stored = { index: "logs-2024", limit: "100" };
  const catalog = { index: "logs-2025" };
  const live = { limit: "500", targetKind: "data_stream" };

  it("lets each later layer override the one before it", () => {
    expect(
      mergeProviderOptions({ layers: [stored, catalog, live] }),
    ).toEqual({ index: "logs-2025", limit: "500" });
  });

  it("skips layers a host has not supplied", () => {
    expect(mergeProviderOptions({ layers: [undefined, stored] })).toEqual(
      stored,
    );
  });

  // targetKind only tells the inspection endpoint which mappings to fetch. It
  // is not a provider option, so it must not reach the stored profile.
  it("drops targetKind from the options a query runs with", () => {
    expect(mergeProviderOptions({ layers: [live] })).toEqual({ limit: "500" });
    expect(
      mergeProviderOptions({ layers: [live], keepTargetKind: true }),
    ).toEqual(live);
  });

  it("pins the active database over whatever the layers carried", () => {
    expect(
      mergeProviderOptions({
        layers: [{ database: "stale" }],
        database: "analytics",
      }),
    ).toEqual({ database: "analytics" });
  });

  // An empty database means the connection's own default, which the backend
  // resolves — sending "" would ask for a database literally named "".
  it("leaves the database alone when none is active", () => {
    expect(
      mergeProviderOptions({ layers: [{ database: "app" }], database: "" }),
    ).toEqual({ database: "app" });
  });

  it("does not mutate the layers it merges", () => {
    mergeProviderOptions({ layers: [live], database: "analytics" });
    expect(live).toEqual({ limit: "500", targetKind: "data_stream" });
  });
});
