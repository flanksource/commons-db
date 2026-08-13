import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { OperationsApiClient, ResolvedOperation } from "@flanksource/clicky-ui";

import { ProfileFilters } from "./reconcileBench";

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
  ] as const)("renders the %s filter form inline", (side, label) => {
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
    expect(html).toContain('aria-label="Region"');
    expect(html).not.toContain('aria-haspopup="menu"');
  });
});
