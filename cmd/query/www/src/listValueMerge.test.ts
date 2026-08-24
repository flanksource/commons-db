import { describe, expect, it } from "vitest";
import {
  mergeIntoMultiFilter,
  parseMultiFilterValue,
  serializeMultiFilterValue,
} from "./listValueMerge";

describe("the wire codec", () => {
  // The Go decoder is query.parseColumnFilterSelection; these cases are the
  // shapes it accepts, so a round trip here is a round trip there.
  it("reads a comma-joined selection, treating ! as an exclusion", () => {
    expect(parseMultiFilterValue("a,!b,c")).toEqual({ a: "include", b: "exclude", c: "include" });
  });

  it("trims whitespace around values and after the exclusion marker", () => {
    expect(parseMultiFilterValue(" a , ! b ")).toEqual({ a: "include", b: "exclude" });
  });

  it("ignores empty segments", () => {
    expect(parseMultiFilterValue("a,,b")).toEqual({ a: "include", b: "include" });
  });

  it("reads an empty selection as nothing selected", () => {
    expect(parseMultiFilterValue("")).toEqual({});
  });

  it("writes includes before excludes so the value is stable", () => {
    expect(serializeMultiFilterValue({ b: "exclude", a: "include", c: "include" })).toBe("a,c,!b");
  });

  it("round-trips a mixed selection", () => {
    const value = "a,c,!b";
    expect(serializeMultiFilterValue(parseMultiFilterValue(value))).toBe(value);
  });

  it("writes nothing for an empty selection", () => {
    expect(serializeMultiFilterValue({})).toBe("");
  });
});

describe("mergeIntoMultiFilter", () => {
  it("replaces the selection by default, which is what loading a file means", () => {
    const result = mergeIntoMultiFilter({ old: "include" }, ["a", "b"], "include", "replace");
    expect(result.next).toEqual({ a: "include", b: "include" });
    expect(result.added).toBe(2);
  });

  it("keeps the existing selection when adding", () => {
    const result = mergeIntoMultiFilter({ old: "include" }, ["a"], "include", "add");
    expect(result.next).toEqual({ old: "include", a: "include" });
  });

  it("applies the chosen mode to every value", () => {
    expect(mergeIntoMultiFilter({}, ["a", "b"], "exclude", "replace").next).toEqual({
      a: "exclude",
      b: "exclude",
    });
  });

  // A file may carry exclusions the same way a typed selection does, so a
  // leading ! wins over the mode chosen for the rest of the file.
  it("honours a ! prefix on a value regardless of the chosen mode", () => {
    expect(mergeIntoMultiFilter({}, ["a", "!b"], "include", "replace").next).toEqual({
      a: "include",
      b: "exclude",
    });
  });

  it("still honours a ! prefix when the file is loaded as exclusions", () => {
    expect(mergeIntoMultiFilter({}, ["!b"], "exclude", "replace").next).toEqual({ b: "exclude" });
  });

  it("counts a value whose mode it changed as flipped, not added", () => {
    const result = mergeIntoMultiFilter({ a: "include" }, ["a"], "exclude", "add");
    expect(result.next).toEqual({ a: "exclude" });
    expect(result.flipped).toBe(1);
    expect(result.added).toBe(0);
  });

  it("counts a value already at the target mode as neither added nor flipped", () => {
    const result = mergeIntoMultiFilter({ a: "include" }, ["a"], "include", "add");
    expect(result.added).toBe(0);
    expect(result.flipped).toBe(0);
  });

  it("drops a bare ! that names no value", () => {
    expect(mergeIntoMultiFilter({}, ["!", "a"], "include", "replace").next).toEqual({
      a: "include",
    });
  });
});
