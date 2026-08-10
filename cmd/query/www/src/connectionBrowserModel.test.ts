import { QueryBrowserExecutionError } from "@flanksource/clicky-ui";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchJSON, mergeProviderOptions } from "./connectionBrowserModel";

afterEach(() => vi.unstubAllGlobals());

it("preserves provider diagnostics from a failed JSON request", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: "query failed",
          diagnostics: {
            provider: "clickhouse",
            request: { query: "SELECT broken" },
            error: "unknown identifier broken",
          },
        }),
        { status: 422, headers: { "Content-Type": "application/json" } },
      ),
    ),
  );

  try {
    await fetchJSON("/query");
    throw new Error("expected fetchJSON to reject");
  } catch (error) {
    expect(error).toBeInstanceOf(QueryBrowserExecutionError);
    expect((error as QueryBrowserExecutionError).message).toBe("query failed");
    expect((error as QueryBrowserExecutionError).diagnostics?.request.query).toBe(
      "SELECT broken",
    );
  }
});

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
