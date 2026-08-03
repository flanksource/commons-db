import { describe, expect, it } from "vitest";
import type { ResolvedOperation } from "@flanksource/clicky-ui";
import { findProfileUpdateOperation, isProfileSurface } from "./editProfileAction";

function operation(
  surface: string,
  verb: "list" | "create" | "update",
  scope: "collection" | "entity",
): ResolvedOperation {
  return {
    path: `/api/v1/${surface}`,
    method: verb === "list" ? "get" : "put",
    operation: {
      responses: {},
      "x-clicky": { surface, verb, scope },
    },
  };
}

describe("profile surface editing", () => {
  it("recognizes only dynamic profile surfaces", () => {
    expect(isProfileSurface("profile-os")).toBe(true);
    expect(isProfileSurface("profiles")).toBe(false);
    expect(isProfileSurface("connection")).toBe(false);
  });

  it("selects the profiles entity update operation", () => {
    const expected = operation("profiles", "update", "entity");
    expect(
      findProfileUpdateOperation([
        operation("profiles", "list", "collection"),
        operation("connection", "update", "entity"),
        expected,
      ]),
    ).toBe(expected);
  });
});
