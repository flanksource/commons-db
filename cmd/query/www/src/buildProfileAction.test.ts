import { describe, expect, it } from "vitest";
import type { ResolvedOperation } from "@flanksource/clicky-ui";
import {
  buildProfileInitialValue,
  findProfileCreateOperation,
} from "./buildProfileAction";

function operation(
  surface: string,
  verb: "list" | "create" | "update",
  scope: "collection" | "entity",
): ResolvedOperation {
  return {
    path: `/api/v1/${surface}`,
    method: verb === "list" ? "get" : "post",
    operation: {
      responses: {},
      "x-clicky": { surface, verb, scope },
    },
  };
}

describe("findProfileCreateOperation", () => {
  it("selects only the collection create action for the profiles surface", () => {
    const expected = operation("profiles", "create", "collection");
    expect(
      findProfileCreateOperation([
        operation("profiles", "list", "collection"),
        operation("connection", "create", "collection"),
        operation("profiles", "update", "entity"),
        expected,
      ]),
    ).toBe(expected);
  });
});

describe("buildProfileInitialValue", () => {
  it("carries a selected OpenSearch index into the profile draft", () => {
    expect(
      buildProfileInitialValue("OS", "opensearch", {
        index: "top_queries-2026.07.13-44509",
      }),
    ).toEqual({
      provider: {
        type: "opensearch",
        connection: "connection://OS",
        options: { index: "top_queries-2026.07.13-44509" },
      },
    });
  });
});
