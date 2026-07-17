import { describe, expect, it } from "vitest";
import type { ResolvedOperation } from "@flanksource/clicky-ui";
import {
  findConnectionCreateOperation,
  isProfileConnectionRequired,
  profileConnectionOptions,
} from "./profileConnectionMapping";

describe("profile connection mapping", () => {
  it("recognizes only structured profile mapping conflicts", () => {
    expect(
      isProfileConnectionRequired({
        code: "profile_connection_required",
        profile: "jms",
        mappingProfile: "jaeger",
        connectionType: "opentelemetry",
        mappingUrl: "/api/v1/profile/jms/connection",
      }),
    ).toBe(true);
    expect(isProfileConnectionRequired({ code: "profile_connection_required" })).toBe(false);
  });

  it("finds the connection create action and formats saved references", () => {
    const create = {
      path: "/api/v1/connection",
      method: "post",
      operation: {
        responses: {},
        "x-clicky": { surface: "connection", scope: "collection", verb: "create" },
      },
    } satisfies ResolvedOperation;
    expect(findConnectionCreateOperation([create])).toBe(create);
    expect(profileConnectionOptions({ options: { "connection://traces": {} } })).toEqual([
      { value: "connection://traces", label: "traces" },
    ]);
  });
});
