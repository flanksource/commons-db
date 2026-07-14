import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  CatalogTree,
  completionForInspection,
  openSearchIndexOptions,
  queryBrowserOptionsSchema,
} from "./connectionBrowser";

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
  it("maps inspected targets into grouped searchable options", () => {
    expect(
      openSearchIndexOptions({
        kind: "opensearch",
        targets: [
          { name: "logs-2026.07.13", kind: "index" },
          { name: "logs-current", kind: "alias" },
          { name: "logs", kind: "data_stream" },
        ],
      }),
    ).toEqual([
      expect.objectContaining({ value: "logs-2026.07.13", group: "Indexes" }),
      expect.objectContaining({ value: "logs-current", group: "Aliases" }),
      expect.objectContaining({ value: "logs", group: "Data streams" }),
    ]);
  });

  it("removes the duplicate free-text index option from OpenSearch", () => {
    const schema = queryBrowserOptionsSchema({
      kind: "query",
      provider: "opensearch",
      optionsSchema: {
        type: "object",
        properties: {
          index: { type: "string", title: "Index" },
          limit: { type: "string", title: "Limit" },
        },
      },
    });

    expect(schema?.properties).not.toHaveProperty("index");
    expect(schema?.properties).toHaveProperty("limit");
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
