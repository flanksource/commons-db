import { describe, expect, it } from "vitest";
import {
  mergeProfileProjection,
  mergeSampledProfileColumns,
  profileEditRoute,
  profileEditSurfaceKey,
  profileColumnResetState,
  profileEditorSections,
  profileRoute,
  profileSampleSignature,
  profileSchemaProjection,
  profileUpdateConflictTarget,
  resetProfileColumns,
  validateProfileEditorDraft,
} from "./profileEditorModel";

describe("profile editor model", () => {
  it("defines a custom sectioned workspace independent of the schema layout", () => {
    expect(profileEditorSections.map((section) => section.id)).toEqual([
      "general",
      "source",
      "columns",
      "parameters",
      "advanced",
      "raw",
    ]);
  });

  it("merges projected schema fields without dropping opaque profile fields", () => {
    const draft = {
      profile: "OS",
      params: [{ name: "host" }],
      trace: { interval: "5s" },
      context: { policy: { query: "select 1" } },
    };

    expect(
      mergeProfileProjection(draft, ["params"], {
        params: [{ name: "namespace" }],
      }),
    ).toEqual({
      profile: "OS",
      params: [{ name: "namespace" }],
      trace: { interval: "5s" },
      context: { policy: { query: "select 1" } },
    });
    expect(profileSchemaProjection(["params"]).properties).toHaveProperty("params");
  });

  it("merges samples by name while retaining configured and missing fields", () => {
    expect(
      mergeSampledProfileColumns(
        [
          { name: "message", type: "string", label: "Message" },
          { name: "legacy", type: "string" },
        ],
        [
          { name: "message", type: "json" },
          { name: "duration", type: "duration" },
        ],
      ),
    ).toEqual([
      { name: "message", type: "string", label: "Message" },
      { name: "legacy", type: "string" },
      { name: "duration", type: "duration" },
    ]);
  });

  it("resets configured columns to the latest sample order and metadata", () => {
    const draft = {
      profile: "OS",
      provider: { type: "opensearch" },
      columns: [
        { name: "message", type: "string", label: "Message", cel: "message.trim()" },
        { name: "manual", type: "boolean" },
      ],
      output: { unwrap: "hits" },
    };
    const sampled = [
      { name: "duration", type: "number" },
      { name: "message", type: "json" },
    ];

    const reset = resetProfileColumns(draft, sampled);

    expect(reset).toEqual({
      ...draft,
      columns: sampled,
    });
    expect(reset.columns).not.toBe(sampled);
    expect(reset.columns?.[0]).not.toBe(sampled[0]);
    expect(() => resetProfileColumns(draft, [])).toThrow(
      "Cannot reset profile columns without sampled columns",
    );
  });

  it("offers reset only for OpenSearch with a current non-empty sample", () => {
    expect(
      profileColumnResetState({
        providerType: "sql",
        sampledColumnCount: 2,
        sampleStale: false,
      }),
    ).toEqual({ visible: false, disabled: true, title: "" });
    expect(
      profileColumnResetState({
        providerType: "opensearch",
        sampledColumnCount: 0,
        sampleStale: false,
      }),
    ).toEqual({
      visible: true,
      disabled: true,
      title: "Run a sample before resetting columns",
    });
    expect(
      profileColumnResetState({
        providerType: "opensearch",
        sampledColumnCount: 2,
        sampleStale: true,
      }),
    ).toEqual({
      visible: true,
      disabled: true,
      title: "Run another sample for the current source and query",
    });
    expect(
      profileColumnResetState({
        providerType: "opensearch",
        sampledColumnCount: 2,
        sampleStale: false,
      }),
    ).toEqual({
      visible: true,
      disabled: false,
      title: "Replace configured columns with the latest sample",
    });
  });

  it("tracks source changes and validates only editor-owned invariants", () => {
    const draft = {
      profile: "OS",
      provider: { type: "sql", connection: "connection://db" },
      query: "select 1",
      columns: [{ name: "id" }],
    };
    expect(profileSampleSignature(draft)).not.toBe(
      profileSampleSignature({ ...draft, query: "select 2" }),
    );
    expect(validateProfileEditorDraft(draft)).toBeNull();
    expect(
      validateProfileEditorDraft({
        ...draft,
        columns: [{ name: "id" }, { name: "id" }],
      }),
    ).toBe('Column name "id" is duplicated');
  });

  it("derives rename routes and structured conflict targets", () => {
    expect(profileRoute("Service Logs.v2")).toBe("/profile-service-logs-v2");
    expect(
      profileUpdateConflictTarget(
        'PROFILE_NAME_CONFLICT: profile "OS" conflicts with existing profile "Linux"',
      ),
    ).toBe("Linux");
  });

  it("round-trips the editor route so a refresh reopens the same profile", () => {
    const surfaceKey = "profile-service-logs-v2";
    expect(profileEditRoute(surfaceKey)).toBe("/profile-service-logs-v2/edit");
    expect(profileEditSurfaceKey(profileEditRoute(surfaceKey))).toBe(surfaceKey);
    expect(profileEditSurfaceKey("/profile-service-logs-v2/edit/")).toBe(surfaceKey);
  });

  it("claims only profile edit routes, leaving detail and collection paths alone", () => {
    expect(profileEditSurfaceKey("/profile-os2")).toBeNull();
    expect(profileEditSurfaceKey("/profiles/edit")).toBeNull();
    expect(profileEditSurfaceKey("/connection/edit")).toBeNull();
    expect(profileEditSurfaceKey("/profile-os2/edit/columns")).toBeNull();
  });
});
