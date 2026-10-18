/**
 * Shared bench and snapshot views. The host owns URLs, navigation, profile
 * loading and authenticated API access; this module owns reconciliation state.
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

import { useState } from "react";
import {
  Button,
  Icon,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import { UiArrowLeft } from "@flanksource/clicky-ui/icons";

import { ReconcileBench, type BenchState } from "./reconcileBench";
import { ReconcileResults } from "./reconcileResults";
import {
  benchQueryForConfig,
  classifySnapshotError,
  findProfileRunOperation,
  initialLane,
  reconcileQueryString,
  RESULTS_PAGE_SIZE,
  resultsViewQueryString,
  type ProfileDocument,
  type ReconcileQuery,
  type ReconcileSnapshot,
  type ResultsView,
} from "./reconcileModel";
import {
  benchKeyExpression,
  useBenchState,
  useDestinations,
  useReconcileOperations,
  useReconcileProfiles,
  useReconcileSnapshot,
  useResultsView,
  useRunReconcile,
  useSaveReconcile,
  type ReconcileOperations,
} from "./reconcileSession";

export interface ReconcileNavigation {
  search: string;
  view: "bench" | "results";
  snapshotId?: string;
  backHref: string;
  benchHref: string;
  snapshotHref: (id: string) => string;
  virtualProfileHref: (snapshot: ReconcileSnapshot, view: ResultsView) => string;
  navigate: (href: string, options?: { replace?: boolean }) => void;
}

export interface ReconcilePageProps {
  client: OperationsApiClient;
  sourceName: string;
  loadProfiles: () => Promise<ProfileDocument[]>;
  profilesQueryKey: readonly unknown[];
  navigation: ReconcileNavigation;
  onProfileSaved?: () => void | Promise<void>;
}

type Blocked = { message: string; error: boolean };

/** Why the page cannot show the bench or the results yet, or null when it can. */
function reconcileBlocked({
  profiles,
  sourceDocument,
  sourceName,
  ops,
}: {
  profiles: { isError: boolean; isLoading: boolean; error: unknown };
  sourceDocument: ProfileDocument | undefined;
  sourceName: string;
  ops: ReconcileOperations;
}): Blocked | null {
  // The failure branch comes first: a profile that fails to load leaves the
  // bench state null, which would otherwise read as "still loading" forever.
  if (profiles.isError || (!profiles.isLoading && sourceDocument == null)) {
    return {
      error: true,
      message: profiles.error instanceof Error
        ? profiles.error.message
        : `No stored profile matches ${sourceName}`,
    };
  }
  if (!ops.operationsLoading && !ops.reconcileAction) {
    return { error: true, message: "Reconciling is unavailable — the profiles reconcile action was not found" };
  }
  if (!ops.operationsLoading && !ops.materializeAction) {
    return { error: true, message: "Exporting is unavailable — the reconcile materialize action was not found" };
  }
  if (!ops.operationsLoading && !ops.snapshotAction) {
    return { error: true, message: "Reading a reconciliation is unavailable — the reconcile-snapshot action was not found" };
  }
  if (ops.operationsLoading || profiles.isLoading) {
    return { error: false, message: "Loading profile…" };
  }
  return null;
}

/**
 * A live bench wins; otherwise the join the snapshot recorded, so Back
 * reopens the run that was read rather than whatever the profile stores now.
 * With neither — an expired snapshot — the bench falls back to the profile's
 * own `reconcile:` block.
 */
function benchHrefFor({
  benchHref,
  state,
  keyExpression,
  snapshot,
}: {
  benchHref: string;
  state: BenchState | null;
  keyExpression: string;
  snapshot: ReconcileSnapshot | undefined;
}): string {
  const benchQuery: ReconcileQuery = state
    ? {
        dest: state.dest,
        cel: keyExpression,
        snapshotAge: state.snapshotAge,
        sourceFilters: state.sourceFilters,
        destFilters: state.destFilters,
      }
    : benchQueryForConfig(snapshot?.reconcile?.config, snapshot?.idle_age);
  return withQuery(benchHref, reconcileQueryString(benchQuery), [
    "dest",
    "cel",
    "snapshot-age",
    "source-filter",
    "dest-filter",
  ]);
}

/** Mount under a QueryClientProvider scoped to one server and authentication context. */
export function ReconcilePage({ client, sourceName, loadProfiles, profilesQueryKey, navigation, onProfileSaved }: ReconcilePageProps) {
  const { view, search } = navigation;
  const ops = useReconcileOperations(client);
  const { profiles, sourceDocument } = useReconcileProfiles({ profilesQueryKey, loadProfiles, sourceName });
  const [state, setState] = useBenchState({ view, sourceDocument, search });
  // The last run this tab produced, so the bench can offer a way back to it.
  // The snapshot itself lives in the query cache, not here — one source of
  // truth for both a fresh run and a cold load.
  const [lastRunId, setLastRunId] = useState("");
  const snapshotId = view === "results" ? navigation.snapshotId ?? "" : lastRunId;
  const snapshot = useReconcileSnapshot({ client, snapshotAction: ops.snapshotAction, sourceName, snapshotId });
  const [resultsView, setResultsView] = useResultsView({ view, snapshot: snapshot.data, search });
  const { destDocument, destNames } = useDestinations({
    documents: profiles.data,
    sourceName,
    destName: state?.dest,
  });
  const keyExpression = benchKeyExpression(state);

  const showResults = (next: ResultsView, id: string, replace: boolean) => {
    setResultsView(next);
    navigation.navigate(withQuery(
      navigation.snapshotHref(id),
      resultsViewQueryString(next),
      ["outcome", "page", "size", "sort", "order"],
    ), { replace });
  };

  const run = useRunReconcile({
    client,
    reconcileAction: ops.reconcileAction,
    sourceName,
    state,
    keyExpression,
    profilesQueryKey,
    onSeeded: (reconciled) => setLastRunId(reconciled.id),
    // Pushed rather than replaced: the bench is where Back goes.
    onReconciled: (reconciled) =>
      showResults(
        { lane: initialLane(reconciled.stats), page: 0, pageSize: RESULTS_PAGE_SIZE },
        reconciled.id,
        false,
      ),
  });
  const save = useSaveReconcile({
    client,
    updateAction: ops.updateAction,
    sourceDocument,
    sourceName,
    state,
    keyExpression,
    profilesQueryKey,
    onProfileSaved,
  });

  const blocked = reconcileBlocked({ profiles, sourceDocument, sourceName, ops });
  if (blocked) return <Message error={blocked.error}>{blocked.message}</Message>;

  if (view === "results") {
    return (
      <ResultsPage
        sourceName={sourceName}
        snapshot={snapshot}
        resultsView={resultsView}
        onView={(next) => showResults(next, snapshotId, true)}
        onBack={() => navigation.navigate(benchHrefFor({
          benchHref: navigation.benchHref,
          state,
          keyExpression,
          snapshot: snapshot.data,
        }))}
        client={client}
        materializeAction={ops.materializeAction!}
        virtualProfileHref={navigation.virtualProfileHref}
      />
    );
  }

  if (state == null) {
    return <Message>Loading profile…</Message>;
  }

  const failure = [run.error, save.error, profiles.error].find((error): error is Error => error instanceof Error);
  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-auto p-4">
      <PageHeader
        onBack={() => navigation.navigate(navigation.backHref)}
        backLabel="Back"
        title={`Reconcile ${sourceName}`}
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
        sourceOperation={findProfileRunOperation(ops.operations, sourceName)}
        destOperation={findProfileRunOperation(ops.operations, state.dest)}
        onRun={() => run.mutate()}
        onSave={() => save.mutate()}
        running={run.isPending}
        saving={save.isPending}
        error={failure?.message ?? ""}
      />

      {/* The bench still knows the run it produced, so returning to it does not
          strand the snapshot behind a re-run. Editing the bench clears it. */}
      {lastRunId !== "" && snapshot.data && (
        <ViewResultsButton
          snapshot={snapshot.data}
          onClick={() =>
            showResults(
              resultsView ?? { lane: initialLane(snapshot.data!.stats), page: 0, pageSize: RESULTS_PAGE_SIZE },
              lastRunId,
              false,
            )
          }
        />
      )}
    </div>
  );
}

function ViewResultsButton({ snapshot, onClick }: { snapshot: ReconcileSnapshot; onClick: () => void }) {
  return (
    <Button variant="outline" size="sm" className="self-start" onClick={onClick}>
      View results ({snapshot.row_count} rows)
    </Button>
  );
}

function ResultsPage({
  sourceName,
  snapshot,
  resultsView,
  onView,
  onBack,
  client,
  materializeAction,
  virtualProfileHref,
}: {
  sourceName: string;
  snapshot: { data: ReconcileSnapshot | undefined; isLoading: boolean; error: unknown };
  resultsView: ResultsView | null;
  onView: (view: ResultsView) => void;
  onBack: () => void;
  client: OperationsApiClient;
  materializeAction: ResolvedOperation;
  virtualProfileHref: ReconcileNavigation["virtualProfileHref"];
}) {
  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-auto p-4">
      {/* Above every branch below, so Back works even when the run cannot be
          read — an expired snapshot still needs a way out. */}
      <PageHeader
        onBack={onBack}
        backLabel="Back to bench"
        title={
          snapshot.data
            ? `${snapshot.data.source} → ${snapshot.data.dest}`
            : `Reconcile ${sourceName}`
        }
        description="What crossed, what didn't, and how late it was."
      />
      <ResultsBody
        snapshot={snapshot.data}
        loading={snapshot.isLoading}
        error={snapshot.error}
        view={resultsView}
        onView={onView}
        client={client}
        materializeAction={materializeAction}
        virtualProfileHref={virtualProfileHref}
      />
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
  virtualProfileHref,
}: {
  snapshot: ReconcileSnapshot | undefined;
  loading: boolean;
  error: unknown;
  view: ResultsView | null;
  onView: (view: ResultsView) => void;
  client: OperationsApiClient;
  materializeAction: ResolvedOperation;
  virtualProfileHref: ReconcileNavigation["virtualProfileHref"];
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
      profileHref={virtualProfileHref}
    />
  );
}

/** Replace one serializer's state while preserving host-owned route parameters. */
function withQuery(href: string, search: string, ownedKeys: string[]): string {
  const [base, hash] = href.split("#", 2);
  const [path, query] = base.split("?", 2);
  const params = new URLSearchParams(query);
  for (const key of ownedKeys) params.delete(key);
  const updates = new URLSearchParams(search);
  updates.forEach((value, key) => params.append(key, value));
  const suffix = params.toString();
  return path + (suffix ? `?${suffix}` : "") + (hash === undefined ? "" : `#${hash}`);
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
