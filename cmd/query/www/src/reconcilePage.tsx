/**
 * The `/profile-<slug>/reconcile` route: build a join on the bench, run it, read
 * the outcome as triage.
 *
 * The run is the profiles `reconcile` action, resolved from the OpenAPI
 * operation rather than a hardcoded path, so the browser and the CLI drive the
 * same code. What the bench produces is exactly what the profile can store under
 * `reconcile:`, which is why "Save on profile" is a write of this same state and
 * not a second shape.
 */

import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Icon,
  useOperations,
  useRouter,
  type OperationsApiClient,
} from "@flanksource/clicky-ui";
import { UiArrowLeft } from "@flanksource/clicky-ui/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { findProfileUpdateOperation } from "./profileUpdateOperation";
import { PROFILES_QUERY_KEY, useProfiles } from "./profilesQuery";
import { ReconcileBench, type BenchState } from "./reconcileBench";
import { ReconcileResults } from "./reconcileResults";
import {
  celForPairings,
  findReconcileAction,
  parseReconcileQuery,
  profileForSurface,
  reconcileQueryString,
  reconcileRoute,
  storedConfig,
  type ProfileDocument,
  type ReconcileResult,
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
    limit: query.limit ?? stored?.limit ?? 0,
    params: stored?.params ?? {},
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
  const queryClient = useQueryClient();
  const { operations, isLoading: operationsLoading } = useOperations(client);
  const updateAction = findProfileUpdateOperation(operations);
  const reconcileAction = findReconcileAction(operations);

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
  const [result, setResult] = useState<ReconcileResult | null>(null);

  // The bench opens on what the profile already stores, so a saved reconcile
  // runs without being retyped.
  useEffect(() => {
    if (state != null || sourceDocument == null) return;
    setState(initialState(sourceDocument, window.location.search));
  }, [sourceDocument, state]);

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

  const run = useMutation({
    mutationFn: async (): Promise<ReconcileResult> => {
      if (!reconcileAction) throw new Error("The reconcile action is unavailable");
      if (!state) throw new Error("The bench is still loading");
      const idParam = reconcileAction.operation["x-clicky"]?.idParam ?? "id";
      const params: Record<string, string> = {
        [idParam]: sourceName,
        dest: state.dest,
        "key-cel": keyExpression,
      };
      if (state.limit > 0) params.limit = String(state.limit);
      const filters = Object.entries(state.params).map(([name, value]) => `${name}=${value}`);
      if (filters.length > 0) params.param = filters.join(",");

      const response = await client.executeCommand(reconcileAction.path, reconcileAction.method, params, {
        Accept: "application/json",
      });
      if (!response.success) {
        throw new Error(response.error ?? response.message ?? "The reconcile failed");
      }
      const parsed = response.parsed;
      if (!parsed || typeof parsed !== "object" || !Array.isArray((parsed as ReconcileResult).rows)) {
        throw new Error("The reconcile returned no result");
      }
      return parsed as ReconcileResult;
    },
    onSuccess: async (reconciled) => {
      setResult(reconciled);
      // The engine resolved both profile documents server-side, so refresh the
      // cached copies the results view renders against — otherwise a stale
      // definition would describe a run that used a newer one.
      await queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY });
      // Keep the run in the URL so it can be shared or reloaded before it is
      // saved onto the profile.
      if (state) {
        router.navigate(
          reconcileRoute(surfaceKey) +
            reconcileQueryString({ dest: state.dest, cel: keyExpression, limit: state.limit }),
          { replace: true },
        );
      }
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
            limit: state.limit,
            params: state.params,
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
  if (operationsLoading || profiles.isLoading || state == null) {
    return <Message>Loading profile…</Message>;
  }

  const failure = [run.error, save.error, profiles.error].find((error): error is Error => error instanceof Error);

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-auto p-4">
      <header className="flex flex-wrap items-center gap-3">
        <Button variant="ghost" size="sm" className="gap-1.5" onClick={() => router.navigate(`/${surfaceKey}`)}>
          <Icon icon={UiArrowLeft} className="size-4" />
          Back
        </Button>
        <div>
          <h1 className="text-lg font-semibold">Reconcile {sourceName || surfaceKey}</h1>
          <p className="text-xs text-muted-foreground">
            Join this profile's rows against another on a shared identity, and see what crossed, what didn't, and
            how late it was.
          </p>
        </div>
      </header>

      <ReconcileBench
        state={state}
        onChange={setState}
        source={sourceDocument}
        dest={destDocument}
        destNames={destNames}
        onRun={() => run.mutate()}
        onSave={() => save.mutate()}
        running={run.isPending}
        saving={save.isPending}
        error={failure?.message ?? ""}
      />

      {result && (
        <section className="flex min-h-0 flex-col gap-2">
          <h2 className="text-sm font-semibold">
            {result.source} → {result.dest}
          </h2>
          <ReconcileResults result={result} source={sourceDocument} dest={destDocument} />
        </section>
      )}
    </div>
  );
}

function Message({ children, error = false }: { children: React.ReactNode; error?: boolean }) {
  return (
    <div className={`grid h-full place-items-center p-8 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>
      {children}
    </div>
  );
}
