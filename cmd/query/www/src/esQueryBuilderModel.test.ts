import { describe, expect, it } from "vitest";
import {
  conditionAt,
  emptyCondition,
  fieldForParam,
  insertAt,
  isEmptySpec,
  isParamValue,
  normalizeOccur,
  removeAt,
  toBuilderMode,
  toRawMode,
  updateAt,
  type EsCondition,
  type EsSearch,
} from "./esQueryBuilderModel";

const tree: EsCondition = {
  op: "bool",
  conditions: [
    { op: "term", field: "level", value: "error" },
    {
      op: "bool",
      occur: "should",
      conditions: [
        { op: "match", field: "message", value: "timeout" },
        { op: "exists", field: "trace.id" },
      ],
    },
  ],
};

describe("condition tree edits", () => {
  it("addresses a nested condition by its index path", () => {
    expect(conditionAt(tree, [1, 0])).toEqual({
      op: "match",
      field: "message",
      value: "timeout",
    });
    expect(conditionAt(tree, [])).toBe(tree);
    expect(conditionAt(tree, [1, 7])).toBeUndefined();
  });

  it("replaces a nested condition without mutating the original tree", () => {
    const next = updateAt(tree, [1, 0], (condition) => ({
      ...condition,
      value: "timed out",
    }));
    expect(conditionAt(next, [1, 0])?.value).toBe("timed out");
    expect(conditionAt(tree, [1, 0])?.value).toBe("timeout");
    // Untouched branches are shared, so React sees a changed identity only
    // along the edited path.
    expect(next.conditions?.[0]).toBe(tree.conditions?.[0]);
    expect(next.conditions?.[1]).not.toBe(tree.conditions?.[1]);
  });

  it("inserts into a group at the requested position", () => {
    const added = emptyCondition("keyword");
    const next = insertAt(tree, [1], 1, added);
    expect(next.conditions?.[1].conditions).toEqual([
      { op: "match", field: "message", value: "timeout" },
      added,
      { op: "exists", field: "trace.id" },
    ]);
    expect(tree.conditions?.[1].conditions).toHaveLength(2);
  });

  it("appends when the position is past the end", () => {
    const next = insertAt(tree, [], 99, emptyCondition("keyword"));
    expect(next.conditions).toHaveLength(3);
    expect(next.conditions?.[2].op).toBe("term");
  });

  it("removes a nested condition and leaves its siblings", () => {
    const next = removeAt(tree, [1, 0]);
    expect(next.conditions?.[1].conditions).toEqual([
      { op: "exists", field: "trace.id" },
    ]);
  });

  it("removing the root leaves an empty group rather than nothing", () => {
    expect(removeAt(tree, [])).toEqual({ op: "bool", conditions: [] });
  });
});

describe("condition defaults", () => {
  it("picks the family's default operator", () => {
    expect(emptyCondition("keyword").op).toBe("term");
    expect(emptyCondition("text").op).toBe("match");
    expect(emptyCondition("date").op).toBe("range");
    expect(emptyCondition("number").op).toBe("range");
    expect(emptyCondition("boolean").op).toBe("term");
  });

  it("defaults an unset occur to filter", () => {
    expect(normalizeOccur(undefined)).toBe("filter");
    expect(normalizeOccur("")).toBe("filter");
    expect(normalizeOccur("must_not")).toBe("must_not");
  });
});

describe("param operands", () => {
  it("recognises a param reference and leaves literals alone", () => {
    expect(isParamValue({ param: "level" })).toBe(true);
    expect(isParamValue({ literal: { param: "level" } })).toBe(false);
    expect(isParamValue("error")).toBe(false);
    expect(isParamValue(undefined)).toBe(false);
    expect(isParamValue({ param: "level", boost: 2 })).toBe(false);
  });
});

describe("spec emptiness", () => {
  const cases: Array<[string, EsSearch | undefined, boolean]> = [
    ["undefined", undefined, true],
    ["no keys", {}, true],
    ["a bare match_all", { query: { op: "match_all" } }, true],
    ["an empty root group", { query: { op: "bool", conditions: [] } }, true],
    ["a group holding a leaf", { query: tree }, false],
    ["sort only", { sort: [{ field: "@timestamp" }] }, false],
    ["size only", { size: 50 }, false],
    ["a preserved aggregation", { aggregations: { byLevel: {} } }, false],
    ["a time field only", { timeField: "@timestamp" }, false],
  ];
  it.each(cases)("treats %s as empty=%s", (_name, spec, empty) => {
    expect(isEmptySpec(spec)).toBe(empty);
  });
});

describe("raw-DSL transition", () => {
  // The server rejects a spec and a raw query together, so each transition has
  // to clear the side it is leaving. Neither direction may leave both set.
  it("hands the compiled DSL to the raw editor and drops the spec", () => {
    expect(
      toRawMode({ query: tree, size: 10 }, '{"query":{"match_all":{}}}'),
    ).toEqual({ search: undefined, query: '{"query":{"match_all":{}}}' });
  });

  it("keeps the raw query when there is no compiled DSL to carry over", () => {
    expect(toRawMode({ query: tree }, "", "{}")).toEqual({
      search: undefined,
      query: "{}",
    });
  });

  it("starts the builder from an empty spec and clears the raw query", () => {
    expect(toBuilderMode()).toEqual({
      search: { query: { op: "bool", conditions: [] } },
      query: "",
    });
  });
});

describe("fieldForParam", () => {
  const bound: EsSearch = {
    query: {
      op: "bool",
      conditions: [
        { op: "term", field: "level", value: "error" },
        {
          op: "bool",
          conditions: [
            { op: "terms", field: "service.name", values: [{ param: "service" }] },
            { op: "range", field: "@timestamp", gte: { param: "since" } },
          ],
        },
      ],
    },
  };

  it("reads the field off the condition a parameter is bound to", () => {
    expect(fieldForParam(bound, "service")).toBe("service.name");
    expect(fieldForParam(bound, "since")).toBe("@timestamp");
  });

  it("has no field for a parameter the specification never references", () => {
    expect(fieldForParam(bound, "unused")).toBeUndefined();
    expect(fieldForParam(bound, "")).toBeUndefined();
    expect(fieldForParam(undefined, "service")).toBeUndefined();
  });
});
