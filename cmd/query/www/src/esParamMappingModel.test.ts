import { describe, expect, it } from "vitest";
import type { EsSearch } from "./esQueryBuilderModel";
import {
  addParamMapping,
  bindParamOperand,
  paramMappings,
  reconcileParamMappings,
  reconcileSearchParamMappings,
  removeParamMapping,
} from "./esParamMappingModel";
import type { ParamDraft } from "./profileWizardModel";

const filterParams: ParamDraft[] = [
  { name: "service", type: "string", role: "filter" },
  { name: "schemes", type: "list", role: "filter" },
];

describe("parameter query mappings", () => {
  it("adds more than one scalar condition for the same parameter", () => {
    const first = addParamMapping({
      search: {},
      params: filterParams,
      name: "service",
      field: "service.name",
    });
    const second = addParamMapping({
      ...first,
      name: "service",
      field: "peer.service",
    });

    expect(second).toEqual({
      search: {
        query: {
          op: "bool",
          conditions: [
            {
              op: "term",
              occur: "filter",
              field: "service.name",
              value: { param: "service" },
              optional: true,
            },
            {
              op: "term",
              occur: "filter",
              field: "peer.service",
              value: { param: "service" },
              optional: true,
            },
          ],
        },
      },
      params: filterParams,
    });
    expect(paramMappings(second.search, "service")).toEqual([
      { path: [0], field: "service.name", operand: "value" },
      { path: [1], field: "peer.service", operand: "value" },
    ]);
  });

  it("moves a list mapping and keeps its native field linked", () => {
    const existing: EsSearch = {
      query: {
        op: "bool",
        conditions: [
          {
            op: "terms",
            field: "old.scheme",
            values: ["literal", { param: "schemes" }],
          },
        ],
      },
    };

    const result = addParamMapping({
      search: existing,
      params: filterParams,
      name: "schemes",
      field: "scheme.id",
    });

    expect(result.search.query?.conditions).toEqual([
      { op: "terms", field: "old.scheme", values: ["literal"] },
      {
        op: "terms",
        occur: "filter",
        field: "scheme.id",
        value: { param: "schemes" },
        optional: true,
      },
    ]);
    expect(result.params[1].field).toBe("scheme.id");
    expect(paramMappings(result.search, "schemes")).toEqual([
      { path: [1], field: "scheme.id", operand: "value" },
    ]);
  });

  it("removes only the selected reference and prunes an empty leaf", () => {
    const search: EsSearch = {
      query: {
        op: "bool",
        conditions: [
          {
            op: "terms",
            field: "scheme.id",
            values: ["literal", { param: "schemes" }],
          },
          { op: "term", field: "service.name", value: { param: "service" } },
        ],
      },
    };

    const list = removeParamMapping({
      search,
      params: filterParams,
      name: "schemes",
      path: [0],
    });
    expect(list.search.query?.conditions).toEqual([
      { op: "terms", field: "scheme.id", values: ["literal"] },
      { op: "term", field: "service.name", value: { param: "service" } },
    ]);
    expect(list.params[1].field).toBeUndefined();

    const scalar = removeParamMapping({
      ...list,
      name: "service",
      path: [1],
    });
    expect(scalar.search.query?.conditions).toEqual([
      { op: "terms", field: "scheme.id", values: ["literal"] },
    ]);
  });

  it("binds a multiple operand canonically without a stale singular value", () => {
    const result = bindParamOperand({
      search: {
        query: {
          op: "terms",
          field: "scheme.id",
          value: "stale",
          values: ["literal"],
        },
      },
      params: filterParams,
      path: [],
      operand: "values",
      name: "schemes",
    });

    expect(result.search.query).toEqual({
      op: "terms",
      field: "scheme.id",
      value: undefined,
      values: [{ param: "schemes" }],
      gt: undefined,
      gte: undefined,
      lt: undefined,
      lte: undefined,
      conditions: undefined,
    });
    expect(result.params[1].field).toBe("scheme.id");
  });

  it("keeps a linked list field synchronized across query tree edits", () => {
    const previousSearch: EsSearch = {
      query: {
        op: "bool",
        conditions: [
          {
            op: "terms",
            field: "scheme.id",
            value: { param: "schemes" },
          },
        ],
      },
    };
    const params = [
      filterParams[0],
      { ...filterParams[1], field: "scheme.id" },
    ];

    const moved = reconcileSearchParamMappings({
      previousSearch,
      nextSearch: {
        query: {
          op: "bool",
          conditions: [
            {
              op: "terms",
              field: "scheme.code",
              value: { param: "schemes" },
            },
          ],
        },
      },
      params,
    });
    expect(moved.params[1].field).toBe("scheme.code");

    const removed = reconcileSearchParamMappings({
      previousSearch: moved.search,
      nextSearch: { query: { op: "bool", conditions: [] } },
      params: moved.params,
    });
    expect(removed.params[1].field).toBeUndefined();
  });

  it("preserves a native-only list field across unrelated query edits", () => {
    const params = [
      filterParams[0],
      { ...filterParams[1], field: "legacy.scheme" },
    ];

    const edit = reconcileSearchParamMappings({
      previousSearch: {},
      nextSearch: {
        query: { op: "term", field: "service.name", value: "payments" },
      },
      params,
    });

    expect(edit.params[1].field).toBe("legacy.scheme");
  });

  it("maps time roles and rejects automatic paging roles", () => {
    const time = addParamMapping({
      search: {},
      params: [{ name: "from", type: "date", role: "time-from" }],
      name: "from",
      field: "startTimeMillis",
    });
    expect(time.search.timeField).toBe("startTimeMillis");

    expect(() =>
      addParamMapping({
        search: {},
        params: [{ name: "limit", type: "number", role: "limit" }],
        name: "limit",
        field: "size",
      }),
    ).toThrow("limit parameter limit cannot map to a query field");
  });
});

describe("parameter definition reconciliation", () => {
  it("renames every operand and gate atomically", () => {
    const search: EsSearch = {
      query: {
        op: "bool",
        conditions: [
          { op: "term", field: "service.name", value: { param: "service" } },
          { op: "exists", field: "error", when: "service" },
        ],
      },
    };
    const next = [{ ...filterParams[0], name: "application" }, filterParams[1]];

    expect(
      reconcileParamMappings({ search, previous: filterParams, next }),
    ).toEqual({
      search: {
        query: {
          op: "bool",
          conditions: [
            {
              op: "term",
              field: "service.name",
              value: { param: "application" },
            },
            { op: "exists", field: "error", when: "application" },
          ],
        },
      },
      params: next,
    });
  });

  it("removes deleted references and preserves unrelated conditions", () => {
    const search: EsSearch = {
      query: {
        op: "bool",
        conditions: [
          { op: "term", field: "service.name", value: { param: "service" } },
          { op: "term", field: "level", value: "error" },
        ],
      },
    };

    expect(
      reconcileParamMappings({
        search,
        previous: filterParams,
        next: [filterParams[1]],
      }),
    ).toEqual({
      search: {
        query: {
          op: "bool",
          conditions: [{ op: "term", field: "level", value: "error" }],
        },
      },
      params: [filterParams[1]],
    });
  });
});
