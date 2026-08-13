/**
 * The connection dashboard's pure shapes and derivations: what the API returns,
 * how connections group into namespace lanes, and the dashboard URL a result
 * context maps to. Kept apart from the components so the derivations stay
 * directly testable and the component file exports components only.
 */

export type ConnectionDashboardHealthState =
  | "healthy"
  | "credentials"
  | "unreachable"
  | "unverifiable"
  | "unknown";

export type ConnectionHealth = {
  state: ConnectionDashboardHealthState;
  detail: string;
  checkedAt: string;
  cached: boolean;
};

export type ConnectionDashboardItem = {
  id: string;
  name: string;
  namespace: string;
  type: string;
  endpoint?: { scheme: string; host: string; path?: string };
  secretCount: number;
  inlineCredential: boolean;
  insecureTLS: boolean;
  // Absent until someone checks: listing reads the database only, so a
  // connection has no health until a check is explicitly requested.
  health?: ConnectionHealth | null;
  profileCount: number;
  updatedAt: string;
};

export type ConnectionDashboardResponse = {
  connections: ConnectionDashboardItem[];
  generatedAt: string;
};

export type ConnectionHealthResult = ConnectionHealth & {
  id: string;
  durationMs: number;
};

export type ConnectionHealthResponse = {
  results: ConnectionHealthResult[];
  generatedAt: string;
};

export type ConnectionDashboardLane = {
  namespace: string;
  connections: ConnectionDashboardItem[];
};

export function groupConnectionDashboardLanes(
  connections: ConnectionDashboardItem[],
): ConnectionDashboardLane[] {
  const lanes = new Map<string, ConnectionDashboardItem[]>();
  for (const connection of connections) {
    const lane = lanes.get(connection.namespace);
    if (lane) lane.push(connection);
    else lanes.set(connection.namespace, [connection]);
  }
  return [...lanes]
    .map(([namespace, items]) => ({
      namespace,
      connections: [...items].sort((a, b) => a.name.localeCompare(b.name)),
    }))
    .sort((a, b) => {
      if (a.namespace === "") return 1;
      if (b.namespace === "") return -1;
      return a.namespace.localeCompare(b.namespace);
    });
}

export const connectionHealthUrl = "/api/v1/connections/health";

export function connectionDashboardUrl(requestUrl?: string): string {
  const target = new URL("/api/v1/connections/dashboard", "http://query.local");
  if (requestUrl) {
    const source = new URL(requestUrl, "http://query.local");
    for (const key of ["type", "types"]) {
      const value = source.searchParams.get(key);
      if (value) target.searchParams.set(key, value);
    }
  }
  return target.pathname + target.search;
}
