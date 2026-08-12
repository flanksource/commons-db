import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ConnectionDashboardLanes } from "./connectionDashboard";
import {
  connectionDashboardUrl,
  groupConnectionDashboardLanes,
  type ConnectionDashboardItem,
} from "./connectionDashboardModel";

function connection(
  overrides: Partial<ConnectionDashboardItem>,
): ConnectionDashboardItem {
  return {
    id: "connection-id",
    name: "warehouse",
    namespace: "acme",
    type: "postgres",
    endpoint: { scheme: "postgres", host: "db.example:5432", path: "/app" },
    secretCount: 0,
    inlineCredential: false,
    insecureTLS: false,
    health: { state: "healthy", detail: "PostgreSQL 17" },
    profileCount: 0,
    updatedAt: "2026-08-09T12:00:00Z",
    ...overrides,
  };
}

describe("connection dashboard lanes", () => {
  it("carries connection type filters into the batched health request", () => {
    expect(
      connectionDashboardUrl("/api/v1/connection?type=postgres&limit=50&offset=100"),
    ).toBe("/api/v1/connections/dashboard?type=postgres");
  });

  it("sorts namespace lanes alphabetically with the unnamespaced lane last", () => {
    const lanes = groupConnectionDashboardLanes([
      connection({ id: "none", name: "orphan", namespace: "" }),
      connection({ id: "beta", name: "beta-db", namespace: "beta" }),
      connection({ id: "alpha-z", name: "zeta", namespace: "alpha" }),
      connection({ id: "alpha-a", name: "alpha", namespace: "alpha" }),
    ]);

    expect(lanes.map((lane) => lane.namespace)).toEqual(["alpha", "beta", ""]);
    expect(lanes[0]?.connections.map((item) => item.name)).toEqual(["alpha", "zeta"]);
  });

  it("renders lane health, risks, usage, and a detail link", () => {
    const html = renderToStaticMarkup(
      <ConnectionDashboardLanes
        connections={[
          connection({
            id: "failing-id",
            name: "warehouse",
            health: { state: "credentials", detail: "Could not resolve credentials" },
            secretCount: 2,
            inlineCredential: true,
            insecureTLS: true,
          }),
          connection({ id: "unused-id", name: "cache" }),
        ]}
      />,
    );

    expect(html).toContain("1 failing");
    expect(html).toContain("2 unused");
    expect(html).toContain("2 secrets");
    expect(html).toContain("Password in URL");
    expect(html).toContain("TLS verification off");
    expect(html).toContain('href="/connection/failing-id"');
    expect(html).toContain('aria-label="Credentials failed"');
  });
});
