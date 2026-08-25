import { describe, expect, it } from "vitest";
import type {
  ConnectionDashboardItem,
  ConnectionHealth,
  ConnectionHealthResult,
} from "./connectionDashboardModel";
import {
  initialHealthMap,
  mergeHealthResults,
  summarizeLaneHealth,
  type ConnectionHealthMap,
} from "./connectionHealth";

const EARLIER = "2026-08-13T10:00:00Z";
const LATER = "2026-08-13T10:05:00Z";

function connection(
  overrides: Partial<ConnectionDashboardItem>,
): ConnectionDashboardItem {
  return {
    id: "warehouse",
    name: "warehouse",
    namespace: "acme",
    type: "postgres",
    secretCount: 0,
    inlineCredential: false,
    insecureTLS: false,
    profileCount: 1,
    updatedAt: "2026-08-09T12:00:00Z",
    ...overrides,
  };
}

function result(
  overrides: Partial<ConnectionHealthResult> & { id: string },
): ConnectionHealthResult {
  return {
    state: "healthy",
    detail: "PostgreSQL 17.2",
    checkedAt: LATER,
    cached: false,
    durationMs: 41,
    ...overrides,
  };
}

describe("mergeHealthResults", () => {
  it("adds a checked connection without disturbing the rest of the fleet", () => {
    const previous: ConnectionHealthMap = {
      cache: { state: "unreachable", detail: "TCP connect failed", checkedAt: EARLIER, cached: false },
    };

    const merged = mergeHealthResults(previous, [result({ id: "warehouse" })]);

    expect(Object.keys(merged).sort()).toEqual(["cache", "warehouse"]);
    expect(merged.cache).toBe(previous.cache);
    expect(merged.warehouse?.state).toBe("healthy");
  });

  it("does not overwrite a newer check with a batch that started before it", () => {
    const previous: ConnectionHealthMap = {
      warehouse: { state: "unreachable", detail: "TCP connect failed", checkedAt: LATER, cached: false },
    };

    const merged = mergeHealthResults(previous, [
      result({ id: "warehouse", state: "healthy", checkedAt: EARLIER }),
    ]);

    expect(merged.warehouse?.state).toBe("unreachable");
  });

  it("strips the transport-only duration from the stored health", () => {
    const merged = mergeHealthResults({}, [result({ id: "warehouse" })]);

    expect(merged.warehouse).toEqual<ConnectionHealth>({
      state: "healthy",
      detail: "PostgreSQL 17.2",
      checkedAt: LATER,
      cached: false,
    });
  });
});

describe("initialHealthMap", () => {
  it("seeds only the rows the server still had a cached check for", () => {
    const warm: ConnectionHealth = {
      state: "healthy",
      detail: "PostgreSQL 17.2",
      checkedAt: EARLIER,
      cached: true,
    };

    const seeded = initialHealthMap([
      connection({ id: "warehouse", health: warm }),
      connection({ id: "cache", health: null }),
      connection({ id: "orders" }),
    ]);

    expect(seeded).toEqual({ warehouse: warm });
  });
});

describe("summarizeLaneHealth", () => {
  it("counts a partially checked lane without treating unchecked rows as healthy", () => {
    const connections = [
      connection({ id: "warehouse", profileCount: 1 }),
      connection({ id: "cache", profileCount: 0 }),
      connection({ id: "orders", profileCount: 0 }),
      connection({ id: "reports", profileCount: 2 }),
    ];
    const health: ConnectionHealthMap = {
      warehouse: { state: "healthy", detail: "PostgreSQL 17.2", checkedAt: LATER, cached: false },
      cache: { state: "credentials", detail: "secret acme/redis not found", checkedAt: LATER, cached: false },
      orders: { state: "unknown", detail: "batch budget expired", checkedAt: LATER, cached: false },
    };

    expect(summarizeLaneHealth(connections, health)).toEqual({
      checked: 2,
      failing: 1,
      unused: 2,
    });
  });

  it("reports an unchecked lane as zero checked rather than throwing", () => {
    expect(summarizeLaneHealth([connection({ id: "warehouse" })], {})).toEqual({
      checked: 0,
      failing: 0,
      unused: 0,
    });
  });
});
