/** Adapt the query app's profile routes to the shared reconciliation screen. */
import { useRouter, type OperationsApiClient } from "@flanksource/clicky-ui";
import { useQueryClient } from "@tanstack/react-query";
import {
  ReconcilePage as SharedReconcilePage,
  profileForSurface,
  reconcileRoute,
  reconcileRouteView,
  reconcileSnapshotRoute,
  virtualProfileHref,
} from "@flanksource/commons-db-ui";
import { fetchProfiles, PROFILES_QUERY_KEY, useProfiles } from "./profilesQuery";

/** Keep the standalone app's URLs and profile cache outside the shared package. */
export function ReconcilePage({ client, surfaceKey }: { client: OperationsApiClient; surfaceKey: string }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const profiles = useProfiles();
  const source = profileForSurface(profiles.data ?? [], surfaceKey);
  const route = reconcileRouteView(router.pathname);
  if (!source?.profile) {
    return <div className="p-8 text-sm" role={profiles.isLoading ? "status" : "alert"}>
      {profiles.isLoading ? "Loading profile…" : profiles.error instanceof Error
        ? profiles.error.message : `No stored profile matches ${surfaceKey}`}
    </div>;
  }
  return <SharedReconcilePage
    key={surfaceKey}
    client={client}
    sourceName={source.profile}
    loadProfiles={fetchProfiles}
    profilesQueryKey={PROFILES_QUERY_KEY}
    onProfileSaved={() => queryClient.invalidateQueries({ queryKey: ["profile-editor", surfaceKey] })}
    navigation={{
      search: window.location.search,
      view: route?.view ?? "bench",
      snapshotId: route?.view === "results" ? route.snapshotId : undefined,
      backHref: `/${surfaceKey}`,
      benchHref: reconcileRoute(surfaceKey),
      snapshotHref: (id) => reconcileSnapshotRoute(surfaceKey, id),
      virtualProfileHref,
      navigate: router.navigate,
    }}
  />;
}
