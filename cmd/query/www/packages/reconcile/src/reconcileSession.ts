/**
 * The reconcile page's state and server calls, as hooks owned by one
 * `ReconcilePage` so bench state survives a trip to the results and back.
 */

import { useEffect, useMemo, useState } from "react";
import {
  useOperations,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { findProfileUpdateOperation } from "./profileUpdateOperation";
import type { BenchState } from "./reconcileBench";
import {
  celForPairings,
  filterFlagValue,
  findReconcileAction,
  findReconcileMaterializeAction,
  findReconcileSnapshotAction,
  initialLane,
  initialReconcileFilters,
  loadReconcileSnapshot,
  parseReconcileQuery,
  parseResultsView,
  reconcileSnapshotQueryKey,
  storedConfig,
  type ProfileDocument,
  type ReconcileSnapshot,
  type ResultsView,
} from "./reconcileModel";

/** Bench state seeded from the profile's stored reconcile, then from the URL. */
function initialState(document: ProfileDocument | undefined, search: string): BenchState {
  const query = parseReconcileQuery(search);
  const stored = document?.reconcile;
  const cel = query.cel ?? stored?.key?.cel ?? "";
  return {
    dest: query.dest ?? stored?.dest ?? "",
    pairings: [],
    mode: cel ? "cel" : "mapped",
    cel,
    snapshotAge: query.snapshotAge ?? "1h",
    ...initialReconcileFilters(stored, query),
  };
}

export function benchKeyExpression(state: BenchState | null): string {
  if (state == null) return "";
  return state.mode === "cel" ? state.cel : celForPairings(state.pairings);
}

export function useReconcileOperations(client: OperationsApiClient) {
  const { operations, isLoading: operationsLoading } = useOperations(client);
  return {
    operations,
    operationsLoading,
    updateAction: findProfileUpdateOperation(operations),
    reconcileAction: findReconcileAction(operations),
    materializeAction: findReconcileMaterializeAction(operations),
    snapshotAction: findReconcileSnapshotAction(operations),
  };
}

export type ReconcileOperations = ReturnType<typeof useReconcileOperations>;

/** One list serves the source document, the destination picker and the destination's fields. */
export function useReconcileProfiles({
  profilesQueryKey,
  loadProfiles,
  sourceName,
}: {
  profilesQueryKey: readonly unknown[];
  loadProfiles: () => Promise<ProfileDocument[]>;
  sourceName: string;
}) {
  // It is the endpoint that returns documents rather than executing anything.
  const profiles = useQuery({ queryKey: profilesQueryKey, queryFn: loadProfiles });
  const sourceDocument = useMemo(
    () => (profiles.data ?? []).find((document) => document.profile === sourceName),
    [profiles.data, sourceName],
  );
  return { profiles, sourceDocument };
}

/** The destination's document and the names the bench can offer as destinations. */
export function useDestinations({
  documents,
  sourceName,
  destName,
}: {
  documents: ProfileDocument[] | undefined;
  sourceName: string;
  destName: string | undefined;
}) {
  const destDocument = useMemo(
    () => (documents ?? []).find((document) => document.profile === destName),
    [documents, destName],
  );
  const destNames = useMemo(() => {
    const names: string[] = [];
    for (const document of documents ?? []) {
      const name = document.profile ?? "";
      if (name !== "" && name !== sourceName) names.push(name);
    }
    return names.sort();
  }, [documents, sourceName]);
  return { destDocument, destNames };
}

export function useBenchState({
  view,
  sourceDocument,
  search,
}: {
  view: "bench" | "results";
  sourceDocument: ProfileDocument | undefined;
  search: string;
}) {
  const [state, setState] = useState<BenchState | null>(null);
  // The bench opens on what the profile already stores, so a saved reconcile
  // runs without being retyped. Gated on the bench route: on a cold results
  // load this would otherwise fill the bench from the profile's stored block a
  // tick later and quietly outrank the snapshot's own config.
  useEffect(() => {
    if (view !== "bench" || state != null || sourceDocument == null) return;
    setState(initialState(sourceDocument, search));
  }, [sourceDocument, state, view, search]);
  return [state, setState] as const;
}

export function useResultsView({
  view,
  snapshot,
  search,
}: {
  view: "bench" | "results";
  snapshot: ReconcileSnapshot | undefined;
  search: string;
}) {
  // Keep immediate interactions responsive even when a host router publishes
  // query-string changes asynchronously; location updates also restore Back.
  const [resultsView, setResultsView] = useState<ResultsView | null>(null);
  // The lane a link asks for, falling back to the one the run would open on —
  // which is only knowable once the snapshot has been read.
  useEffect(() => {
    if (view !== "results" || snapshot == null) return;
    setResultsView(parseResultsView(search, initialLane(snapshot.stats)));
  }, [snapshot, search, view]);
  return [resultsView, setResultsView] as const;
}

export function useReconcileSnapshot({
  client,
  snapshotAction,
  sourceName,
  snapshotId,
}: {
  client: OperationsApiClient;
  snapshotAction: ResolvedOperation | undefined;
  sourceName: string;
  snapshotId: string;
}) {
  return useQuery({
    queryKey: reconcileSnapshotQueryKey(snapshotId),
    queryFn: () => loadReconcileSnapshot(client, snapshotAction!, sourceName, snapshotId),
    enabled: snapshotId !== "" && snapshotAction != null && sourceName !== "",
    // A snapshot is immutable for its whole life, so a cache hit is never stale
    // and a run navigating to its own results never refetches.
    staleTime: Infinity,
    retry: 0,
  });
}

function runParams({
  idParam,
  sourceName,
  state,
  keyExpression,
}: {
  idParam: string;
  sourceName: string;
  state: BenchState;
  keyExpression: string;
}): Record<string, string> {
  const params: Record<string, string> = {
    [idParam]: sourceName,
    dest: state.dest,
    "key-cel": keyExpression,
  };
  if (state.snapshotAge.trim()) params["snapshot-age"] = state.snapshotAge.trim();
  const sourceFilters = filterFlagValue(state.sourceFilters);
  const destFilters = filterFlagValue(state.destFilters);
  if (sourceFilters) params["source-filter"] = sourceFilters;
  if (destFilters) params["dest-filter"] = destFilters;
  return params;
}

async function runReconcile({
  client,
  reconcileAction,
  sourceName,
  state,
  keyExpression,
}: {
  client: OperationsApiClient;
  reconcileAction: ResolvedOperation | undefined;
  sourceName: string;
  state: BenchState | null;
  keyExpression: string;
}): Promise<ReconcileSnapshot> {
  if (!reconcileAction) throw new Error("The reconcile action is unavailable");
  if (!state) throw new Error("The bench is still loading");
  const idParam = reconcileAction.operation["x-clicky"]?.idParam ?? "id";
  const response = await client.executeCommand(
    reconcileAction.path,
    reconcileAction.method,
    runParams({ idParam, sourceName, state, keyExpression }),
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error ?? response.message ?? "The reconcile failed");
  }
  const parsed = response.parsed as Partial<ReconcileSnapshot> | undefined;
  if (!parsed || typeof parsed.id !== "string" || typeof parsed.profile !== "string" || !Array.isArray(parsed.columns)) {
    throw new Error("The reconcile returned no snapshot profile");
  }
  return parsed as ReconcileSnapshot;
}

export function useRunReconcile({
  client,
  reconcileAction,
  sourceName,
  state,
  keyExpression,
  profilesQueryKey,
  onSeeded,
  onReconciled,
}: {
  client: OperationsApiClient;
  reconcileAction: ResolvedOperation | undefined;
  sourceName: string;
  state: BenchState | null;
  keyExpression: string;
  profilesQueryKey: readonly unknown[];
  /** Called once the run's snapshot is in the cache, before the profile list is refreshed. */
  onSeeded: (snapshot: ReconcileSnapshot) => void;
  onReconciled: (snapshot: ReconcileSnapshot) => void;
}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => runReconcile({ client, reconcileAction, sourceName, state, keyExpression }),
    onSuccess: async (reconciled) => {
      // Seeded before navigating, so the results page renders from cache on its
      // first paint instead of re-reading a snapshot already in hand.
      queryClient.setQueryData(reconcileSnapshotQueryKey(reconciled.id), reconciled);
      onSeeded(reconciled);
      // The engine resolved both profile documents server-side, so refresh the
      // cached copies the results view renders against — otherwise a stale
      // definition would describe a run that used a newer one.
      await queryClient.invalidateQueries({ queryKey: profilesQueryKey });
      onReconciled(reconciled);
    },
  });
}

async function saveReconcile({
  client,
  updateAction,
  sourceDocument,
  sourceName,
  state,
  keyExpression,
}: {
  client: OperationsApiClient;
  updateAction: ResolvedOperation | undefined;
  sourceDocument: ProfileDocument | undefined;
  sourceName: string;
  state: BenchState | null;
  keyExpression: string;
}): Promise<void> {
  if (!updateAction) throw new Error("Profile updates are unavailable");
  if (!state || !sourceDocument) throw new Error("The bench is still loading");
  if (!client.submitForm) throw new Error("Profile updates are unavailable");
  const idParam = updateAction.operation["x-clicky"]?.idParam ?? "id";
  const response = await client.submitForm(
    updateAction.path,
    updateAction.method,
    {
      ...sourceDocument,
      reconcile: storedConfig({
        dest: state.dest,
        cel: keyExpression,
        sourceFilters: state.sourceFilters,
        destFilters: state.destFilters,
      }),
      [idParam]: sourceName,
    },
    { Accept: "application/json+clicky" },
  );
  if (!response.success) {
    throw new Error(response.error ?? response.message ?? "Saving the reconcile failed");
  }
}

export function useSaveReconcile({
  client,
  updateAction,
  sourceDocument,
  sourceName,
  state,
  keyExpression,
  profilesQueryKey,
  onProfileSaved,
}: {
  client: OperationsApiClient;
  updateAction: ResolvedOperation | undefined;
  sourceDocument: ProfileDocument | undefined;
  sourceName: string;
  state: BenchState | null;
  keyExpression: string;
  profilesQueryKey: readonly unknown[];
  onProfileSaved?: () => void | Promise<void>;
}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      saveReconcile({ client, updateAction, sourceDocument, sourceName, state, keyExpression }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: profilesQueryKey });
      await onProfileSaved?.();
    },
  });
}
