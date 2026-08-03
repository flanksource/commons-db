import { describe, expect, it } from "vitest";
import type { BrowserDescriptor } from "./connectionBrowserModel";
import {
  navigatorTabs,
  supportsQueryBuilder,
} from "./connectionQueryWorkspace";

const searchSchema = {
  type: "object" as const,
  properties: {
    search: {
      type: "object" as const,
      "x-clicky-component": "es-query-builder",
      "x-es-operators": [
        { op: "term", label: "term", arity: "single", fieldTypes: ["keyword"] },
      ],
    },
  },
};

const openSearch: BrowserDescriptor = {
  kind: "query",
  provider: "opensearch",
  language: "json",
  catalog: true,
  optionsSchema: searchSchema,
};

const sql: BrowserDescriptor = {
  kind: "query",
  provider: "sql",
  language: "sql",
  catalog: true,
};

describe("supportsQueryBuilder", () => {
  it("accepts a source whose options schema carries an operator catalog", () => {
    expect(supportsQueryBuilder(openSearch)).toBe(true);
  });

  it("rejects a source with no options schema", () => {
    expect(supportsQueryBuilder(sql)).toBe(false);
  });

  it("rejects an options schema that describes no structured search", () => {
    expect(
      supportsQueryBuilder({
        ...openSearch,
        optionsSchema: { type: "object", properties: { index: { type: "string" } } },
      }),
    ).toBe(false);
  });
});

describe("navigatorTabs", () => {
  it("offers Catalog and Filters for a structured source", () => {
    expect(navigatorTabs({ descriptor: openSearch, builder: true })).toEqual([
      { id: "catalog", label: "Catalog" },
      { id: "filters", label: "Filters" },
    ]);
  });

  it("offers Catalog alone for a source with no structured search", () => {
    expect(navigatorTabs({ descriptor: sql, builder: false })).toEqual([
      { id: "catalog", label: "Catalog" },
    ]);
  });

  it("offers Filters alone when the source has no catalog to browse", () => {
    expect(
      navigatorTabs({ descriptor: { ...openSearch, catalog: false }, builder: true }),
    ).toEqual([{ id: "filters", label: "Filters" }]);
  });

  it("offers no navigator when there is neither a catalog nor a builder", () => {
    expect(
      navigatorTabs({ descriptor: { ...sql, catalog: false }, builder: false }),
    ).toEqual([]);
  });
});
