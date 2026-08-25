import { describe, expect, it } from "vitest";
import type { ClickyColumn, ClickyRow } from "@flanksource/clicky-ui/clicky";
import {
  configuredColumnName,
  discoverJsonFieldCandidates,
  mergePromotedColumns,
} from "./profileJsonFields";

const columns: ClickyColumn[] = [
  { name: "metadata", label: "Metadata", type: "json" },
  { name: "tags", label: "Tags", type: "key_values" },
  { name: "attributes", label: "Attributes", type: "key_value" },
  { name: "message", label: "Message", type: "string" },
];

const row: ClickyRow = {
  cells: {
    metadata: {
      kind: "text",
      plain: JSON.stringify({
        enabled: true,
        user: { email: "alice@example.com", age: 37 },
        ignored: ["unstable"],
      }),
    },
    tags: {
      kind: "text",
      plain: JSON.stringify([
        { key: "http.response.status_code", type: "int64", value: 200 },
        { key: "error", type: "bool", value: false },
      ]),
    },
    attributes: {
      kind: "text",
      plain: "service.name: checkout\ncache.bytes: 2048",
      children: [
        {
          kind: "map",
          fields: [
            {
              name: "service.name",
              label: "service.name",
              value: { kind: "text", plain: "checkout", text: "checkout" },
            },
            {
              name: "cache.bytes",
              label: "cache.bytes",
              value: { kind: "text", plain: "2048", text: "2048" },
            },
          ],
        },
      ],
    },
    message: { kind: "text", plain: "not json" },
  },
};

describe("profile JSON field promotion", () => {
  it("discovers nested object leaves while skipping ordinary arrays", () => {
    const candidates = discoverJsonFieldCandidates(columns, row);

    expect(
      candidates
        .filter((candidate) => candidate.source === "metadata")
        .map(({ name, type, sample }) => ({ name, type, sample })),
    ).toEqual([
      { name: "enabled", type: "boolean", sample: true },
      { name: "user.age", type: "number", sample: 37 },
      { name: "user.email", type: "string", sample: "alice@example.com" },
    ]);
    expect(candidates.some((candidate) => candidate.name.includes("ignored"))).toBe(false);
  });

  it("discovers telemetry key-value arrays with scalar types", () => {
    const candidates = discoverJsonFieldCandidates(columns, row);

    expect(
      candidates
        .filter((candidate) => candidate.source === "tags")
        .map(({ name, type }) => ({ name, type })),
    ).toEqual([
      { name: "error", type: "boolean" },
      { name: "http.response.status_code", type: "number" },
    ]);
  });

  it("discovers native KeyValue maps from Clicky map nodes", () => {
    const candidates = discoverJsonFieldCandidates(columns, row);

    expect(
      candidates
        .filter((candidate) => candidate.source === "attributes")
        .map(({ name, sample }) => ({ name, sample })),
    ).toEqual([
      { name: "cache.bytes", sample: 2048 },
      { name: "service.name", sample: "checkout" },
    ]);
    expect(
      candidates.find((candidate) => candidate.name === "service.name")?.cel,
    ).toBe(
      `'attributes' in row ? jsonpath("$['service.name']", type(row['attributes']) == string ? row['attributes'].JSON() : row['attributes']) : ''`,
    );
  });

  it("discovers KeyValues maps rendered by Clicky while retaining array CEL", () => {
    const pairRow: ClickyRow = {
      cells: {
        tags: {
          kind: "text",
          children: [
            {
              kind: "map",
              fields: [
                {
                  name: "http.status_code",
                  value: { kind: "text", plain: "503", text: "503" },
                },
              ],
            },
          ],
        },
      },
    };

    const candidate = discoverJsonFieldCandidates(columns, pairRow)[0];
    expect({ name: candidate.name, sample: candidate.sample }).toEqual({
      name: "http.status_code",
      sample: 503,
    });
    expect(candidate.cel).toContain("$[?(@.key == 'http.status_code')].value");
  });

  it("generates guarded CEL for encoded objects and key-value arrays", () => {
    const candidates = discoverJsonFieldCandidates(columns, row);

    expect(candidates.find((candidate) => candidate.name === "user.email")?.cel).toBe(
      `'metadata' in row ? jsonpath("$['user']['email']", type(row['metadata']) == string ? row['metadata'].JSON() : row['metadata']) : ''`,
    );
    expect(
      candidates.find(
        (candidate) => candidate.name === "http.response.status_code",
      )?.cel,
    ).toBe(
      `'tags' in row ? jsonpath("$[?(@.key == 'http.response.status_code')].value", type(row['tags']) == string ? row['tags'].JSONArray() : row['tags']) : ''`,
    );
  });

  it("recognizes an already configured expression even when it was renamed", () => {
    const candidate = discoverJsonFieldCandidates(columns, row).find(
      (item) => item.name === "user.email",
    );

    expect(
      configuredColumnName(candidate!, [
        { name: "email", label: "User email", cel: candidate!.cel },
      ]),
    ).toBe("email");
  });

  it("appends reviewed columns and rejects duplicate names", () => {
    const existing = [{ name: "message", type: "string" }];
    const additions = [
      {
        name: "user.email",
        label: "User email",
        type: "string",
        cel: "jsonpath(...) ",
      },
    ];

    expect(mergePromotedColumns(existing, additions)).toEqual([
      existing[0],
      additions[0],
    ]);
    expect(() =>
      mergePromotedColumns(existing, [{ ...additions[0], name: "message" }]),
    ).toThrow('Column name "message" is already configured');
  });

  it("ignores malformed JSON and structured values on string columns", () => {
    const malformed: ClickyRow = {
      cells: {
        metadata: { kind: "text", plain: "{" },
        tags: { kind: "text", plain: "[]" },
        message: { kind: "text", plain: JSON.stringify({ hidden: true }) },
      },
    };

    expect(discoverJsonFieldCandidates(columns, malformed)).toEqual([]);
  });
});
