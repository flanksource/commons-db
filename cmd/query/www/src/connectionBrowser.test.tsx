import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  CatalogTree,
  completionForInspection,
  openSearchIndexOptions,
  openSearchTargetKind,
  queryBrowserOptionsSchema,
  withTarget,
} from "@flanksource/clicky-ui/profiles";

describe("connection browser inspection completion", () => {
  it("maps SQL inspection data to shared QueryBrowser completion", () => {
    const completion = completionForInspection({
      kind: "sql",
      dialect: "postgresql",
      defaultSchema: "public",
      schemas: [
        {
          name: "public",
          relations: [
            {
              name: "users",
              type: "table",
              columns: [{ name: "email", dataType: "text" }],
            },
          ],
        },
      ],
    });

    expect(completion).toEqual(
      expect.objectContaining({
        kind: "sql",
        dialect: "postgresql",
        defaultSchema: "public",
        schemas: [expect.objectContaining({ name: "public" })],
      }),
    );
  });

  it("maps selected OpenSearch fields without flattening capabilities", () => {
    const completion = completionForInspection(
      { kind: "opensearch", targets: [{ name: "logs", kind: "alias" }] },
      {
        kind: "opensearch",
        selected: {
          target: { name: "logs", kind: "alias" },
          fields: [
            {
              name: "service.name",
              types: ["keyword"],
              searchable: true,
              aggregatable: true,
            },
          ],
        },
      },
    );

    expect(completion).toEqual({
      kind: "json-fields",
      vocabulary: "opensearch",
      fields: [
        expect.objectContaining({ name: "service.name", types: ["keyword"] }),
      ],
    });
  });
});

describe("OpenSearch index picker", () => {
  const rotated = {
    kind: "opensearch" as const,
    targets: [
      {
        name: "logs-2026.07.12,logs-2026.07.13",
        kind: "group" as const,
        pattern: "logs-*",
        members: ["logs-2026.07.12", "logs-2026.07.13"],
        count: 2,
      },
      { name: "logs-2026.07.13", kind: "index" as const, pattern: "logs-*" },
      { name: "logs-current", kind: "alias" as const },
      { name: "logs", kind: "data_stream" as const },
    ],
  };

  it("leads with exact rotation groups and lists concrete indexes last", () => {
    expect(openSearchIndexOptions(rotated)).toEqual([
      expect.objectContaining({
        value: "logs-2026.07.12,logs-2026.07.13",
        group: "Index groups",
        label: "logs-* · 2 indexes",
        selectedLabel: "logs-*",
      }),
      expect.objectContaining({ value: "logs-current", group: "Aliases" }),
      expect.objectContaining({ value: "logs", group: "Data streams" }),
      expect.objectContaining({ value: "logs-2026.07.13", group: "Indexes" }),
    ]);
  });

  it("resolves a picked target's kind, treating a typed wildcard as a pattern", () => {
    expect(openSearchTargetKind(rotated, "logs-current")).toBe("alias");
    expect(openSearchTargetKind(rotated, "jaeger-*")).toBe("pattern");
    expect(openSearchTargetKind(rotated, "typo")).toBe("");
  });

  it("applies a pick over the author's other options and clears on empty", () => {
    const options = { limit: "200", index: "logs-old", targetKind: "index" };

    expect(
      withTarget(options, {
        option: "index",
        value: "logs-*",
        targetKind: "pattern",
      }),
    ).toEqual({
      limit: "200",
      index: "logs-*",
      targetKind: "pattern",
    });
    expect(
      withTarget(options, {
        option: "index",
        value: "",
        targetKind: "",
      }),
    ).toEqual({
      limit: "200",
    });
  });

  it("removes the duplicate free-text index option when a target picker exists", () => {
    const optionsSchema = {
      type: "object" as const,
      properties: {
        index: { type: "string" as const, title: "Index" },
        limit: { type: "string" as const, title: "Limit" },
      },
    };
    const picked = queryBrowserOptionsSchema({
      kind: "query",
      provider: "opensearch",
      target: { kind: "index", label: "Index", option: "index" },
      optionsSchema,
    });
    const unpicked = queryBrowserOptionsSchema({
      kind: "query",
      provider: "opensearch",
      optionsSchema,
    });

    expect(picked?.properties).not.toHaveProperty("index");
    expect(picked?.properties).toHaveProperty("limit");
    expect(unpicked?.properties).toHaveProperty("index");
  });

  it("hands limit to the query builder rather than repeating it as a generic field", () => {
    const optionsSchema = {
      type: "object" as const,
      properties: {
        limit: { type: "string" as const, title: "Limit" },
        address: { type: "string" as const, title: "Address" },
        search: {
          type: "object" as const,
          "x-clicky-component": "es-query-builder",
        },
      },
    };
    const built = queryBrowserOptionsSchema({
      kind: "query",
      provider: "opensearch",
      optionsSchema,
    });

    expect(built?.properties).not.toHaveProperty("limit");
    expect(built?.properties).not.toHaveProperty("search");
    expect(built?.properties).toHaveProperty("address");
  });

  it("keeps limit in the options form for a source with no query builder", () => {
    const built = queryBrowserOptionsSchema({
      kind: "query",
      provider: "loki",
      optionsSchema: {
        type: "object" as const,
        properties: { limit: { type: "string" as const, title: "Limit" } },
      },
    });

    expect(built?.properties).toHaveProperty("limit");
  });
});

describe("CatalogTree", () => {
  it("renders the database switcher and preserves empty schemas", () => {
    const html = renderToStaticMarkup(
      <CatalogTree
        nodes={[{ id: "public", label: "public", kind: "schema" }]}
        loading={false}
        error={null}
        databases={["app", "postgres"]}
        database="postgres"
        onDatabaseChange={() => undefined}
        onSelect={() => undefined}
      />,
    );

    expect(html).toContain("Database");
    expect(html).toContain('<option value="app">app</option>');
    expect(html).toContain("public");
  });

  it("shows an explicit empty catalog state", () => {
    const html = renderToStaticMarkup(
      <CatalogTree
        nodes={[]}
        loading={false}
        error={null}
        databases={[]}
        database=""
        onDatabaseChange={() => undefined}
        onSelect={() => undefined}
      />,
    );
    expect(html).toContain("No catalog objects found.");
  });

  it("shows the catalog request error details", () => {
    const html = renderToStaticMarkup(
      <CatalogTree
        nodes={[]}
        loading={false}
        error={new Error("OpenSearch rejected the request: 401 Unauthorized")}
        databases={[]}
        database=""
        onDatabaseChange={() => undefined}
        onSelect={() => undefined}
      />,
    );

    expect(html).toContain("Unable to load catalog");
    expect(html).toContain("OpenSearch rejected the request: 401 Unauthorized");
    expect(html).toContain('role="alert"');
  });

  it("opens schemas but keeps relation columns collapsed initially", () => {
    const html = renderToStaticMarkup(
      <CatalogTree
        nodes={[
          {
            id: "public",
            label: "public",
            kind: "schema",
            children: [
              {
                id: "public.users",
                label: "users",
                kind: "table",
                query: "SELECT * FROM users",
                children: [
                  {
                    id: "public.users.email",
                    label: "email · text",
                    kind: "column",
                  },
                ],
              },
            ],
          },
        ]}
        loading={false}
        error={null}
        databases={[]}
        database=""
        onDatabaseChange={() => undefined}
        onSelect={() => undefined}
      />,
    );

    expect(html).toContain("public");
    expect(html).toContain("users");
    expect(html).not.toContain("email · text");
    expect(html).toContain('aria-label="Expand"');
  });
});
