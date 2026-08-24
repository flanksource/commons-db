/**
 * The reconcile surface's two routes: `/profile-<slug>/reconcile` builds a join
 * on the bench, and `/profile-<slug>/reconcile/<snapshot-id>` reads what it
 * found.
 *
 * The run is the profiles `reconcile` action, resolved from the OpenAPI
 * operation rather than a hardcoded path, so the browser and the CLI drive the
 * same code. What the bench produces is exactly what the profile can store under
 * `reconcile:`, which is why "Save on profile" is a write of this same state and
 * not a second shape.
 *
 * The results URL names the snapshot and nothing else, because the snapshot
 * stores the join that produced it. That is what makes the URL shareable and
 * reload-safe, and it is where Back gets the bench state from: the run is read
 * back from the server rather than restated in the query string.
 */

import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Icon,
  useOperations,
  useRouter,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { UiArrowLeft } from "@flanksource/clicky-ui/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { findProfileUpdateOperation } from "./profileUpdateOperation";
import { PROFILES_QUERY_KEY, useProfiles } from "./profilesQuery";
import { ReconcileBench, type BenchState } from "./reconcileBench";
import { ReconcileResults } from "./reconcileResults";
import {
  benchQueryForConfig,
  celForPairings,
  classifySnapshotError,
  filterFlagValue,
  findProfileRunOperation,
  findReconcileAction,
  findReconcileMaterializeAction,
  findReconcileSnapshotAction,
  initialLane,
  initialReconcileFilters,
  loadReconcileSnapshot,
  parseReconcileQuery,
  parseResultsView,
  profileForSurface,
  reconcileQueryString,
  reconcileRoute,
  reconcileRouteView,
  reconcileSnapshotQueryKey,
  reconcileSnapshotRoute,
  RESULTS_PAGE_SIZE,
  resultsViewQueryString,
  storedConfig,
  type ProfileDocument,
  type ReconcileQuery,
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

export function ReconcilePage({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const router = useRouter();
  const route = reconcileRouteView(router.pathname);
  const view = route?.view ?? "bench";
  const queryClient = useQueryClient();
  const { operations, isLoading: operationsLoading } = useOperations(client);
  const updateAction = findProfileUpdateOperation(operations);
  const reconcileAction = findReconcileAction(operations);
  const materializeAction = findReconcileMaterializeAction(operations);
  const snapshotAction = findReconcileSnapshotAction(operations);

  // One list serves the source document, the destination picker and the
  // destination's fields — and it is the endpoint that returns documents rather
  // than executing anything.
  const profiles = useProfiles();
  const sourceDocument = useMemo(
    () => profileForSurface(profiles.data ?? [], surfaceKey),
    [profiles.data, surfaceKey],
  );
  const sourceName = sourceDocument?.profile ?? "";

  const [state, setState] = useState<BenchState | null>(null);
  // The last run this tab produced, so the bench can offer a way back to it.
  // The snapshot itself lives in the query cache, not here — one source of
  // truth for both a fresh run and a cold load.
  const [lastRunId, setLastRunId] = useState("");
  // The browser router only reports the pathname, so a query-only navigation
  // does not re-render: React state owns the view and the URL is written to
  // match it.
  const [resultsView, setResultsView] = useState<ResultsView | null>(null);

  const snapshotId = route?.view === "results" ? route.snapshotId : lastRunId;
  const snapshot = useQuery({
    queryKey: reconcileSnapshotQueryKey(snapshotId),
    queryFn: () => loadReconcileSnapshot(client, snapshotAction!, sourceName, snapshotId),
    enabled: snapshotId !== "" && snapshotAction != null && sourceName !== "",
    // A snapshot is immutable for its whole life, so a cache hit is never stale
    // and a run navigating to its own results never refetches.
    staleTime: Infinity,
    retry: 0,
  });

  // The bench opens on what the profile already stores, so a saved reconcile
  // runs without being retyped. Gated on the bench route: on a cold results
  // load this would otherwise fill the bench from the profile's stored block a
  // tick later and quietly outrank the snapshot's own config.
  useEffect(() => {
    if (view !== "bench" || state != null || sourceDocument == null) return;
    setState(initialState(sourceDocument, window.location.search));
  }, [sourceDocument, state, view]);

  // The lane a link asks for, falling back to the one the run would open on —
  // which is only knowable once the snapshot has been read.
  useEffect(() => {
    if (resultsView != null || snapshot.data == null) return;
    setResultsView(parseResultsView(window.location.search, initialLane(snapshot.data.stats)));
  }, [resultsView, snapshot.data]);

  const destDocument = useMemo(
    () => (profiles.data ?? []).find((document) => document.profile === state?.dest),
    [profiles.data, state?.dest],
  );
  const destNames = useMemo(() => {
    const names: string[] = [];
    for (const document of profiles.data ?? []) {
      const name = document.profile ?? "";
      if (name !== "" && name !== sourceName) names.push(name);
    }
    return names.sort();
  }, [profiles.data, sourceName]);

  const keyExpression = state == null ? "" : state.mode === "cel" ? state.cel : celForPairings(state.pairings);
  const sourceRunOperation = findProfileRunOperation(operations, sourceName);
  const destRunOperation = findProfileRunOperation(operations, state?.dest ?? "");

  // A live bench wins; otherwise the join the snapshot recorded, so Back
  // reopens the run that was read rather than whatever the profile stores now.
  // With neither — an expired snapshot — the bench falls back to the profile's
  // own `reconcile:` block.
  const benchQuery: ReconcileQuery = state
    ? {
        dest: state.dest,
        cel: keyExpression,
        snapshotAge: state.snapshotAge,
        sourceFilters: state.sourceFilters,
        destFilters: state.destFilters,
      }
    : benchQueryForConfig(snapshot.data?.reconcile?.config, snapshot.data?.idle_age);
  const benchHref = reconcileRoute(surfaceKey) + reconcileQueryString(benchQuery);
  const showResults = (next: ResultsView, id: string, replace: boolean) => {
    setResultsView(next);
    router.navigate(reconcileSnapshotRoute(surfaceKey, id) + resultsViewQueryString(next), { replace });
  };

  const run = useMutation({
    mutationFn: async (): Promise<ReconcileSnapshot> => {
      if (!reconcileAction) throw new Error("The reconcile action is unavailable");
      if (!state) throw new Error("The bench is still loading");
      const idParam = reconcileAction.operation["x-clicky"]?.idParam ?? "id";
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

      const response = await client.executeCommand(reconcileAction.path, reconcileAction.method, params, {
        Accept: "application/json",
      });
      if (!response.success) {
        throw new Error(response.error ?? response.message ?? "The reconcile failed");
      }
      const parsed = response.parsed as Partial<ReconcileSnapshot> | undefined;
      if (!parsed || typeof parsed.id !== "string" || typeof parsed.profile !== "string" || !Array.isArray(parsed.columns)) {
        throw new Error("The reconcile returned no snapshot profile");
      }
      return parsed as ReconcileSnapshot;
    },
    onSuccess: async (reconciled) => {
      // Seeded before navigating, so the results page renders from cache on its
      // first paint instead of re-reading a snapshot already in hand.
      queryClient.setQueryData(reconcileSnapshotQueryKey(reconciled.id), reconciled);
      setLastRunId(reconciled.id);
      // The engine resolved both profile documents server-side, so refresh the
      // cached copies the results view renders against — otherwise a stale
      // definition would describe a run that used a newer one.
      await queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY });
      // Pushed rather than replaced: the bench is where Back goes.
      showResults(
        { lane: initialLane(reconciled.stats), page: 0, pageSize: RESULTS_PAGE_SIZE },
        reconciled.id,
        false,
      );
    },
  });

  const save = useMutation({
    mutationFn: async () => {
      if (!updateAction) throw new Error("Profile updates are unavailable");
      if (!state || !sourceDocument) throw new Error("The bench is still loading");
      if (!client.submitForm) throw new Error("Profile updates are unavailable");
      const document = sourceDocument;
      const idParam = updateAction.operation["x-clicky"]?.idParam ?? "id";
      const response = await client.submitForm(
        updateAction.path,
        updateAction.method,
        {
          ...document,
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
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY }),
        queryClient.invalidateQueries({ queryKey: ["profile-editor", surfaceKey] }),
      ]);
    },
  });

  // The failure branch comes first: a profile that fails to load leaves the
  // bench state null, which would otherwise read as "still loading" forever.
  if (profiles.isError || (!profiles.isLoading && sourceDocument == null)) {
    return (
      <Message error>
        {profiles.error instanceof Error
          ? profiles.error.message
          : `No stored profile matches ${surfaceKey}`}
      </Message>
    );
  }
  if (!operationsLoading && !reconcileAction) {
    return <Message error>Reconciling is unavailable — the profiles reconcile action was not found</Message>;
  }
  if (!operationsLoading && !materializeAction) {
    return <Message error>Exporting is unavailable — the reconcile materialize action was not found</Message>;
  }
  if (!operationsLoading && !snapshotAction) {
    return <Message error>Reading a reconciliation is unavailable — the reconcile-snapshot action was not found</Message>;
  }
  if (operationsLoading || profiles.isLoading) {
    return <Message>Loading profile…</Message>;
  }

  const failure = [run.error, save.error, profiles.error].find((error): error is Error => error instanceof Error);

  if (view === "results") {
    return (
      <div className="flex h-full min-h-0 flex-col gap-4 overflow-auto p-4">
        {/* Above every branch below, so Back works even when the run cannot be
            read — an expired snapshot still needs a way out. */}
        <PageHeader
          onBack={() => router.navigate(benchHref)}
          backLabel="Back to bench"
          title={
            snapshot.data
              ? `${snapshot.data.source} → ${snapshot.data.dest}`
              : `Reconcile ${sourceName || surfaceKey}`
          }
          description="What crossed, what didn't, and how late it was."
        />
        <ResultsBody
          snapshot={snapshot.data}
          loading={snapshot.isLoading}
          error={snapshot.error}
          view={resultsView}
          onView={(next) => showResults(next, snapshotId, true)}
          client={client}
          materializeAction={materializeAction!}
        />
      </div>
    );
  }

  if (state == null) {
    return <Message>Loading profile…</Message>;
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-auto p-4">
      <PageHeader
        onBack={() => router.navigate(`/${surfaceKey}`)}
        backLabel="Back"
        title={`Reconcile ${sourceName || surfaceKey}`}
        description="Join this profile's rows against another on a shared identity, and see what crossed, what didn't, and how late it was."
      />

      <ReconcileBench
        state={state}
        onChange={(next) => {
          setState(next);
          setLastRunId("");
        }}
        source={sourceDocument}
        dest={destDocument}
        destNames={destNames}
        client={client}
        sourceOperation={sourceRunOperation}
        destOperation={destRunOperation}
        onRun={() => run.mutate()}
        onSave={() => save.mutate()}
        running={run.isPending}
        saving={save.isPending}
        error={failure?.message ?? ""}
      />

      {/* The bench still knows the run it produced, so returning to it does not
          strand the snapshot behind a re-run. Editing the bench clears it. */}
      {lastRunId !== "" && snapshot.data && (
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          onClick={() =>
            showResults(
              resultsView ?? { lane: initialLane(snapshot.data!.stats), page: 0, pageSize: RESULTS_PAGE_SIZE },
              lastRunId,
              false,
            )
          }
        >
          View results ({snapshot.data.row_count} rows)
        </Button>
      )}
    </div>
  );
}

/**
 * The results body, once the header is already on screen.
 *
 * Expiry is told apart from absence deliberately: a snapshot ageing out is a
 * lifecycle event the reader should shrug at and re-run, while an id that was
 * never here means the link itself is wrong.
 */
function ResultsBody({
  snapshot,
  loading,
  error,
  view,
  onView,
  client,
  materializeAction,
}: {
  snapshot: ReconcileSnapshot | undefined;
  loading: boolean;
  error: unknown;
  view: ResultsView | null;
  onView: (view: ResultsView) => void;
  client: OperationsApiClient;
  materializeAction: ResolvedOperation;
}) {
  if (error) {
    switch (classifySnapshotError(error)) {
      case "expired":
        return (
          <Message>
            This reconciliation has expired — snapshots are kept for a limited time. Go back to the bench and
            run it again.
          </Message>
        );
      case "missing":
        return (
          <Message error>
            No reconciliation with this id — it expired long ago, or it was never created on this server.
          </Message>
        );
      default:
        return <Message error>{error instanceof Error ? error.message : String(error)}</Message>;
    }
  }
  if (loading || snapshot == null || view == null) {
    return <Message>Loading reconciliation…</Message>;
  }
  return (
    <ReconcileResults
      client={client}
      snapshot={snapshot}
      materializeAction={materializeAction}
      view={view}
      onView={onView}
    />
  );
}

function PageHeader({
  onBack,
  backLabel,
  title,
  description,
}: {
  onBack: () => void;
  backLabel: string;
  title: string;
  description: string;
}) {
  return (
    <header className="flex flex-wrap items-center gap-3">
      <Button variant="ghost" size="sm" className="gap-1.5" onClick={onBack}>
        <Icon icon={UiArrowLeft} className="size-4" />
        {backLabel}
      </Button>
      <div>
        <h1 className="text-lg font-semibold">{title}</h1>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
    </header>
  );
}

function Message({ children, error = false }: { children: React.ReactNode; error?: boolean }) {
  return (
    <div className={`grid h-full place-items-center p-8 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>
      {children}
    </div>
  );
}
