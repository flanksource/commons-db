import { describe, expect, it, vi } from "vitest";
import type {
  OperationsApiClient,
  ResolvedOperation,
} from "@flanksource/clicky-ui";
import { promoteProfileColumns } from "./profileColumnPromotion";

const getAction: ResolvedOperation = {
  path: "/api/v1/profiles/{id}",
  method: "get",
  operation: { responses: {}, "x-clicky": { surface: "profiles", verb: "get", scope: "entity" } },
};

const updateAction: ResolvedOperation = {
  path: "/api/v1/profiles",
  method: "put",
  operation: {
    responses: {},
    "x-clicky": {
      surface: "profiles",
      verb: "update",
      scope: "entity",
      idParam: "id",
    },
  },
};

describe("profile column promotion persistence", () => {
  it("loads the latest profile and appends columns without dropping fields", async () => {
    const executeCommand = vi.fn(async () => ({
      success: true,
      exitCode: 0,
      parsed: {
        profile: "OS",
        query: "match all",
        provider: { type: "opensearch", connection: "connection://OS" },
        columns: [{ name: "message", type: "string" }],
        opaque: { preserved: true },
      },
    }));
    const submitForm = vi.fn(async () => ({ success: true, exitCode: 0 }));
    const client = { executeCommand, submitForm } as unknown as OperationsApiClient;

    await promoteProfileColumns({
      client,
      getAction,
      updateAction,
      surfaceKey: "profile-os",
      additions: [
        {
          name: "http.response.status_code",
          type: "number",
          cel: "jsonpath(...) ",
        },
      ],
    });

    expect(executeCommand).toHaveBeenCalledWith(
      "/api/v1/profiles/{id}",
      "get",
      { id: "profile-os" },
      { Accept: "application/json" },
    );
    expect(submitForm).toHaveBeenCalledWith(
      "/api/v1/profiles",
      "put",
      {
        profile: "OS",
        query: "match all",
        provider: { type: "opensearch", connection: "connection://OS" },
        columns: [
          { name: "message", type: "string" },
          {
            name: "http.response.status_code",
            type: "number",
            cel: "jsonpath(...) ",
          },
        ],
        opaque: { preserved: true },
        id: "profile-os",
      },
      { Accept: "application/json+clicky" },
    );
  });

  it("surfaces update failures", async () => {
    const client = {
      executeCommand: vi.fn(async () => ({
        success: true,
        exitCode: 0,
        parsed: {
          profile: "OS",
          provider: { type: "opensearch" },
          columns: [],
        },
      })),
      submitForm: vi.fn(async () => ({
        success: false,
        exitCode: 1,
        error: "profile update failed",
      })),
    } as unknown as OperationsApiClient;

    await expect(
      promoteProfileColumns({
        client,
        getAction,
        updateAction,
        surfaceKey: "profile-os",
        additions: [{ name: "enabled", type: "boolean", cel: "jsonpath(...)" }],
      }),
    ).rejects.toThrow("profile update failed");
  });
});
