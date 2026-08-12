/**
 * The entry point to the reconcile route: a button beside Edit on a profile
 * surface, with that profile fixed as the source.
 *
 * The bench needs the viewport, a URL and the back button, so — like the profile
 * editor — it is a route of its own rather than the generic action modal that
 * prints the raw response.
 */

import {
  Button,
  Icon,
  useOperations,
  useRouter,
  type OperationsApiClient,
} from "@flanksource/clicky-ui";
import { UiDiff } from "@flanksource/clicky-ui/icons";

import { findReconcileAction, reconcileRoute } from "./reconcileModel";

export function ReconcileButton({
  client,
  surfaceKey,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
}) {
  const router = useRouter();
  const { operations, isLoading } = useOperations(client);
  const available = findReconcileAction(operations) != null;

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={isLoading || !available}
      title={
        available
          ? "Join this profile against another on a shared identity"
          : "Reconciling is unavailable"
      }
      onClick={() => router.navigate(reconcileRoute(surfaceKey))}
    >
      <Icon icon={UiDiff} className="size-4" />
      Reconcile
    </Button>
  );
}
