/**
 * Client state for on-demand connection health checks.
 *
 * Listing connections never probes — health arrives only when the operator asks
 * for it, one row, one lane, or the whole fleet at a time. The derivations are
 * kept pure and separate from the components so a partially-checked fleet (the
 * normal state) stays directly testable.
 */

import { useMutation } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import {
  connectionHealthUrl,
  type ConnectionDashboardItem,
  type ConnectionHealth,
  type ConnectionHealthResponse,
  type ConnectionHealthResult,
} from "./connectionDashboardModel";

export type ConnectionHealthMap = Record<string, ConnectionHealth>;

/** initialHealthMap seeds from the inventory's already-warm cache entries. */
export function initialHealthMap(
  connections: ConnectionDashboardItem[],
): ConnectionHealthMap {
  const health: ConnectionHealthMap = {};
  for (const connection of connections) {
    if (connection.health) health[connection.id] = connection.health;
  }
  return health;
}

/**
 * mergeHealthResults folds a batch response into the current map. A result is
 * dropped when the map already holds a newer check for that connection, so a
 * slow fleet-wide batch cannot overwrite a row the operator re-checked while it
 * was still running.
 */
export function mergeHealthResults(
  previous: ConnectionHealthMap,
  results: ConnectionHealthResult[],
): ConnectionHealthMap {
  const merged = { ...previous };
  for (const { id, durationMs: _durationMs, ...health } of results) {
    const existing = merged[id];
    if (existing && Date.parse(existing.checkedAt) > Date.parse(health.checkedAt)) {
      continue;
    }
    merged[id] = health;
  }
  return merged;
}

export type LaneHealthSummary = {
  checked: number;
  failing: number;
  unused: number;
};

/**
 * summarizeLaneHealth counts a lane without assuming every row has been checked.
 * "failing" covers only the states that indicate a real problem — an unchecked
 * or indeterminate connection is neither healthy nor failing.
 */
export function summarizeLaneHealth(
  connections: ConnectionDashboardItem[],
  health: ConnectionHealthMap,
): LaneHealthSummary {
  let checked = 0;
  let failing = 0;
  let unused = 0;
  for (const connection of connections) {
    const state = health[connection.id]?.state;
    if (state && state !== "unknown") checked += 1;
    if (state === "credentials" || state === "unreachable") failing += 1;
    if (connection.profileCount === 0) unused += 1;
  }
  return { checked, failing, unused };
}

export type ConnectionHealthCheck = {
  health: ConnectionHealthMap;
  pending: Set<string>;
  error: Error | null;
  seed: (connections: ConnectionDashboardItem[]) => void;
  check: (ids: string[], options?: { force?: boolean }) => void;
};

export function useConnectionHealth(): ConnectionHealthCheck {
  const [health, setHealth] = useState<ConnectionHealthMap>({});
  const [pending, setPending] = useState<Set<string>>(new Set());

  const mutation = useMutation({
    mutationFn: async ({ ids, force }: { ids: string[]; force: boolean }) => {
      const response = await fetch(connectionHealthUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ ids, force }),
      });
      if (!response.ok) {
        const message = (await response.text()).trim();
        throw new Error(message || `Health check failed: ${response.status}`);
      }
      const payload = (await response.json()) as Partial<ConnectionHealthResponse>;
      if (!Array.isArray(payload.results)) {
        throw new Error("Health check response is missing results");
      }
      return payload.results;
    },
    onSuccess: (results) => setHealth((current) => mergeHealthResults(current, results)),
    onSettled: (_data, _error, variables) =>
      setPending((current) => {
        const next = new Set(current);
        for (const id of variables.ids) next.delete(id);
        return next;
      }),
  });

  const check = useCallback(
    (ids: string[], options?: { force?: boolean }) => {
      if (ids.length === 0) return;
      setPending((current) => new Set([...current, ...ids]));
      mutation.mutate({ ids, force: options?.force ?? false });
    },
    [mutation],
  );

  const seed = useCallback(
    (connections: ConnectionDashboardItem[]) =>
      setHealth((current) => ({ ...initialHealthMap(connections), ...current })),
    [],
  );

  return { health, pending, error: mutation.error, seed, check };
}
