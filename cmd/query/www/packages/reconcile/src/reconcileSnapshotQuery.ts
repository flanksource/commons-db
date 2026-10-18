import {
  type ExecutionResponse,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useCallback } from "react";

import { materializeParams } from "./reconcileExportParams";
import type { ReconcileSnapshot, ResultsView } from "./reconcileModel";

/** Prefix of the cached snapshot pages, distinct from the descriptor's ["reconcile-snapshot", <id>]. */
export const SNAPSHOT_PAGE_QUERY_KEY = "reconcile-snapshot-page";

export type Materialize = (
  root: ReconcileSnapshot,
  profile: string,
  columns: string[],
  cel: string,
) => Promise<ReconcileSnapshot>;

export function snapshotPageParams(view: ResultsView): Record<string, string> {
  return {
    "filter.outcome": view.lane,
    limit: String(view.pageSize),
    offset: String(view.page * view.pageSize),
    // The snapshot is paged by the server, so the order has to be too: sorting
    // the hundred rows on screen would reorder the page and nothing else.
    ...(view.sort ? { sort: view.sort, order: view.desc ? "desc" : "asc" } : {}),
  };
}

export async function prepareSnapshotDownload(
  materialize: Materialize,
  root: ReconcileSnapshot,
  active: ReconcileSnapshot,
  columns: string[],
): Promise<{ url: string; label: string }> {
  const projected = await materialize(root, active.profile, columns, "");
  return { url: projected.url, label: `${root.source} to ${root.dest} reconciliation` };
}

export type SnapshotPageResponse = ExecutionResponse;

function isSnapshot(value: unknown): value is ReconcileSnapshot {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<ReconcileSnapshot>;
  return typeof candidate.id === "string" && typeof candidate.profile === "string" &&
    typeof candidate.url === "string" && Array.isArray(candidate.columns);
}

/** Runs the materialize action: a projection of a snapshot, optionally transformed by CEL. */
export function useMaterialize(
  client: OperationsApiClient,
  materializeAction: ResolvedOperation,
): Materialize {
  return useCallback(async (root, profile, columns, expression) => {
    const idParam = materializeAction.operation["x-clicky"]?.idParam ?? "id";
    const response = await client.executeCommand(
      materializeAction.path,
      materializeAction.method,
      { [idParam]: profile, ...materializeParams(root, profile, columns, expression) },
      { Accept: "application/json" },
    );
    if (!response.success) {
      throw new Error(response.error ?? response.message ?? "Materializing reconciliation results failed");
    }
    if (!isSnapshot(response.parsed)) throw new Error("The materialize action returned no snapshot profile");
    return response.parsed;
  }, [client, materializeAction]);
}

/** One server-paged, server-sorted page of the active snapshot profile. */
export function useSnapshotPage({
  client,
  active,
  view,
}: {
  client: OperationsApiClient;
  active: ReconcileSnapshot;
  view: ResultsView;
}) {
  return useQuery({
    // Distinct from ["reconcile-snapshot", <id>], which holds the descriptor:
    // one invalidateQueries prefix would otherwise hit both.
    queryKey: [
      SNAPSHOT_PAGE_QUERY_KEY,
      active.profile,
      view.lane,
      view.page,
      view.pageSize,
      view.sort ?? "",
      view.desc ?? false,
    ],
    queryFn: async () => {
      const response = await client.executeCommand(
        active.url,
        "get",
        snapshotPageParams(view),
        { Accept: "application/json+clicky" },
      );
      if (!response.success) {
        throw new Error(response.error ?? response.message ?? "Loading reconciliation results failed");
      }
      if (!response.parsed || typeof response.parsed !== "object") {
        throw new Error("The reconciliation profile returned no Clicky result");
      }
      return response;
    },
    retry: false,
  });
}
