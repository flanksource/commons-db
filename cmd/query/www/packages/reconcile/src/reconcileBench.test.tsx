import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { OperationsApiClient, ResolvedOperation } from "@flanksource/clicky-ui";

import { ProfileFilters, ReconcileBench, type BenchState } from "./reconcileBench";
import type { ProfileDocument } from "./reconcileModel";

const operation = {
  path: "/api/v1/profile/profile-orders",
  method: "get",
  operation: {
    responses: {},
    parameters: [
      {
        name: "region",
        in: "query" as const,
        schema: { type: "string", enum: ["eu", "us"] },
        "x-clicky": { role: "filter" as const },
      },
    ],
  },
} satisfies ResolvedOperation;

const client = {} as OperationsApiClient;

describe("reconcile profile filters", () => {
  it.each([
    ["source", "Source filters"],
    ["dest", "Destination filters"],
  ] as const)("renders the %s filters with the table FilterBar", (side, label) => {
    const html = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <ProfileFilters
          side={side}
          client={client}
          operation={operation}
          values={{}}
          onChange={() => {}}
        />
      </QueryClientProvider>,
    );

    expect(html).toContain(label);
    expect(html).toContain('data-slot="filter-bar"');
    expect(html).toContain('data-filter-bar-item="region"');
    expect(html).toContain('aria-label="Region"');
  });
});

describe("reconcile field candidates", () => {
  const state: BenchState = {
    dest: "orders-in",
    pairings: [],
    mode: "mapped",
    cel: "",
    snapshotAge: "1h",
    sourceFilters: {},
    destFilters: {},
  };
  const source = {
    profile: "orders-out",
    columns: [{ name: "scheme", label: "Scheme" }],
  } satisfies ProfileDocument;
  const dest = {
    profile: "orders-in",
    columns: [{ name: "scheme", label: "scheme_id" }],
  } satisfies ProfileDocument;

  it("renders each display name with the raw field used by the join", () => {
    const html = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <ReconcileBench
          state={state}
          onChange={() => {}}
          source={source}
          dest={dest}
          destNames={["orders-in"]}
          client={client}
          sourceOperation={undefined}
          destOperation={undefined}
          onRun={() => {}}
          onSave={() => {}}
          running={false}
          saving={false}
          error=""
        />
      </QueryClientProvider>,
    );

    expect(html).toContain(">Scheme (scheme)<");
    expect(html).toContain(">scheme_id (scheme)<");
  });
});
