import { describe, expect, it } from "vitest";
import type { ResolvedOperation } from "@flanksource/clicky-ui";
import {
  findProfileInspectOperation,
  profileInspectInitialValues,
} from "./profileInspectOperation";

function action(surface: string, actionName: string): ResolvedOperation {
  return {
    path: `/api/v1/${surface}/{id}/${actionName}`,
    method: "post",
    operation: {
      responses: {},
      "x-clicky": {
        surface,
        scope: "entity",
        verb: "action",
        actionName,
        idParam: "id",
      },
    },
  };
}

describe("profile inspection action", () => {
  it("selects the inspect action using Clicky metadata", () => {
    const expected = action("profiles", "inspect");
    expect(
      findProfileInspectOperation([
        action("connection", "inspect"),
        action("profiles", "run"),
        expected,
      ]),
    ).toBe(expected);
  });

  it("addresses the action by the stored profile name", () => {
    expect(
      profileInspectInitialValues(action("profiles", "inspect"), {
        profile: "Gavel Sessions",
      }),
    ).toEqual({ id: "Gavel Sessions" });
  });
});
