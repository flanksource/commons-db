import { afterEach, describe, expect, it, vi } from "vitest";
import { makeBrowserFilterLookup } from "./browserFilterValues";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch(response: unknown, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status < 400,
    status,
    json: async () => response,
    text: async () => (typeof response === "string" ? response : ""),
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("makeBrowserFilterLookup", () => {
  const request = {
    filterKey: "filter.region",
    search: "us",
    limit: 20,
    query: "SELECT region FROM orders",
    options: { database: "app" },
    filters: { "filter.env": "prod" },
    columns: [{ name: "region", filterKey: "filter.region" }],
  };

  // The suggestions have to come back scoped, so everything that decides which
  // rows a run returns travels with the ask: the query, its options, the rest of
  // the selection, and the columns the source said it could narrow on.
  it("asks the connection's own browser with the whole query context", async () => {
    const fetchMock = stubFetch({
      options: [{ value: "us-east", count: 12 }],
      total: 40,
      truncated: true,
    });

    const lookup = makeBrowserFilterLookup("/api/v1/connection/abc/browser");
    expect(lookup).toBeDefined();
    const result = await lookup?.(request);

    expect(result).toEqual({ options: [{ value: "us-east", count: 12 }] });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/connection/abc/browser/filters/values");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      query: request.query,
      options: request.options,
      filters: request.filters,
      columns: request.columns,
      filterKey: "filter.region",
      search: "us",
      limit: 20,
    });
  });

  // A source that answered nothing offers nothing. Reading `undefined` as an
  // empty list here would leave the field rendering an option list of undefined
  // rows rather than an empty one.
  it("reads a response with no options as no options", async () => {
    stubFetch({});
    const lookup = makeBrowserFilterLookup("/api/v1/connection/abc/browser");
    await expect(lookup?.(request)).resolves.toEqual({ options: [] });
  });

  it("has no lookup to offer without a browser to ask", () => {
    expect(makeBrowserFilterLookup("")).toBeUndefined();
  });

  // A refusal is the source saying the ask was wrong — a field it cannot
  // enumerate, a query it cannot scope. Swallowing it would leave the field
  // silently empty, which reads as "no values exist".
  it("surfaces the source's refusal", async () => {
    stubFetch("cannot enumerate a range filter", 422);
    const lookup = makeBrowserFilterLookup("/api/v1/connection/abc/browser");
    await expect(lookup?.(request)).rejects.toThrow(
      "cannot enumerate a range filter",
    );
  });
});
