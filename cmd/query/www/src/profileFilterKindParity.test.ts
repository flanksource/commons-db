import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { PROFILE_FILTER_KIND_OPTIONS } from "@flanksource/clicky-ui/profiles";

/**
 * The filter kinds exist three times: the Go enum (query.ColumnFilterKindValues),
 * the labels the profile schema carries for them, and the hand-written mirror
 * clicky-ui's column editor renders from. Go's own tests pin the first two to
 * each other; nothing pinned the third, so a kind added on the server reached
 * the editor as a value with no label, or not at all.
 *
 * This reads the generated schema rather than a fixture of it, so the guard
 * fails on the commit that regenerates the schema instead of one release later.
 */
const kind = JSON.parse(
  readFileSync(new URL("../../../../schemas/src/profile.json", import.meta.url), "utf8"),
).properties.columns.items.properties.filter.properties.kind as {
  enum: string[];
  "x-enum-labels": Record<string, string>;
};

describe("the profile editor's filter kinds", () => {
  it("offers exactly the kinds the server compiles, in the server's order", () => {
    expect(PROFILE_FILTER_KIND_OPTIONS.map((option) => option.value)).toEqual(kind.enum);
  });

  it("names each of them as the schema does", () => {
    expect(Object.fromEntries(PROFILE_FILTER_KIND_OPTIONS.map((o) => [o.value, o.label]))).toEqual(
      kind["x-enum-labels"],
    );
  });
});
