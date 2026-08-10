import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  OperationsApiClient,
  ResolvedOperation,
} from "@flanksource/clicky-ui";
import { loadProfileDocument, promoteProfileColumns } from "./profileColumnPromotion";

afterEach(() => vi.unstubAllGlobals());

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

it("loads the stored document from the profile collection instead of executing the profile", async () => {
  const document = {
    profile: "ClickHouse Logs",
    provider: { type: "clickhouse", connection: "connection://warehouse" },
  };
  const fetch = vi.fn(async () => ({
    ok: true,
    json: async () => [document],
  }));
  vi.stubGlobal("fetch", fetch);

  await expect(loadProfileDocument("profile-clickhouse-logs")).resolves.toBe(document);
  expect(fetch).toHaveBeenCalledWith("/api/v1/profiles", {
    headers: { Accept: "application/json" },
  });
});

describe("profile column promotion persistence", () => {
  it("loads the latest profile and appends columns without dropping fields", async () => {
    const fetch = vi.fn(async () => ({
      ok: true,
      json: async () => [{
        profile: "OS",
        query: "match all",
        provider: { type: "opensearch", connection: "connection://OS" },
        columns: [{ name: "message", type: "string" }],
        opaque: { preserved: true },
      }],
    }));
    vi.stubGlobal("fetch", fetch);
    const submitForm = vi.fn(async () => ({ success: true, exitCode: 0 }));
    const client = { submitForm } as unknown as OperationsApiClient;

    await promoteProfileColumns({
      client,
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

    expect(fetch).toHaveBeenCalledWith("/api/v1/profiles", {
      headers: { Accept: "application/json" },
    });
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
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true,
      json: async () => [{
        profile: "OS",
        provider: { type: "opensearch" },
        columns: [],
      }],
    })));
    const client = {
      submitForm: vi.fn(async () => ({
        success: false,
        exitCode: 1,
        error: "profile update failed",
      })),
    } as unknown as OperationsApiClient;

    await expect(
      promoteProfileColumns({
        client,
        updateAction,
        surfaceKey: "profile-os",
        additions: [{ name: "enabled", type: "boolean", cel: "jsonpath(...)" }],
      }),
    ).rejects.toThrow("profile update failed");
  });
});
