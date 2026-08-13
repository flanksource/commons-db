import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ConnectionDashboardLanes } from "./connectionDashboard";
import {
  connectionDashboardUrl,
  groupConnectionDashboardLanes,
  type ConnectionDashboardItem,
} from "./connectionDashboardModel";
import type { ConnectionHealthMap } from "./connectionHealth";

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
    profileCount: 0,
    updatedAt: "2026-08-09T12:00:00Z",
    ...overrides,
  };
}

describe("connection dashboard lanes", () => {
  it("carries connection type filters into the inventory request", () => {
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

  it("offers a check control per row and claims no verdict before one is run", () => {
    const html = renderToStaticMarkup(
      <ConnectionDashboardLanes
        connections={[connection({ id: "unchecked-id", name: "warehouse" })]}
      />,
    );

    expect(html).toContain('aria-label="Check health"');
    expect(html).toContain("0/1 checked");
    expect(html).not.toContain("failing");
    expect(html).toContain("not checked");
  });

  it("renders lane health, risks, usage, and a detail link once checked", () => {
    const health: ConnectionHealthMap = {
      "failing-id": {
        state: "credentials",
        detail: "Could not resolve credentials",
        checkedAt: "2026-08-13T10:00:00Z",
        cached: false,
      },
    };

    const html = renderToStaticMarkup(
      <ConnectionDashboardLanes
        health={health}
        connections={[
          connection({
            id: "failing-id",
            name: "warehouse",
            secretCount: 2,
            inlineCredential: true,
            insecureTLS: true,
          }),
          connection({ id: "unused-id", name: "cache" }),
        ]}
      />,
    );

    expect(html).toContain("1 failing");
    expect(html).toContain("1/2 checked");
    expect(html).toContain("2 unused");
    expect(html).toContain("2 secrets");
    expect(html).toContain("Password in URL");
    expect(html).toContain("TLS verification off");
    expect(html).toContain('aria-label="Credentials failed — check again"');
    // The dot is a button, so it must be a sibling of the link rather than
    // nested inside it — an anchor would otherwise swallow the click.
    expect(html).toContain('href="/connection/failing-id"');
    expect(html.indexOf('aria-label="Credentials failed')).toBeLessThan(
      html.indexOf('href="/connection/failing-id"'),
    );
  });
});
