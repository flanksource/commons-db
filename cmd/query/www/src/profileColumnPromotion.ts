import type {
  OperationsApiClient,
  ResolvedOperation,
} from "@flanksource/clicky-ui";
import type { ProfileColumn } from "./profileWizardModel";
import { mergePromotedColumns } from "./profileJsonFields";
import { fetchProfiles } from "./profilesQuery";
import { profileForSurface } from "./reconcileModel";

type PromoteProfileColumnsOptions = {
  client: OperationsApiClient;
  updateAction: ResolvedOperation;
  surfaceKey: string;
  additions: ProfileColumn[];
};

export async function loadProfileDocument(
  surfaceKey: string,
): Promise<Record<string, unknown>> {
  const document = profileForSurface(await fetchProfiles(), surfaceKey);
  if (!document) {
    throw new Error(`Profile ${surfaceKey} was not found in the profile collection`);
  }
  return document;
}

export function profileColumns(document: Record<string, unknown>): ProfileColumn[] {
  if (document.columns == null) return [];
  if (!Array.isArray(document.columns)) {
    throw new Error("Profile columns must be an array");
  }
  return document.columns as ProfileColumn[];
}

export async function promoteProfileColumns({
  client,
  updateAction,
  surfaceKey,
  additions,
}: PromoteProfileColumnsOptions): Promise<Record<string, unknown>> {
  if (!client.submitForm) {
    throw new Error("Profile updates are unavailable");
  }
  const profile = await loadProfileDocument(surfaceKey);
  const idParam = updateAction.operation["x-clicky"]?.idParam ?? "id";
  const body = {
    ...profile,
    columns: mergePromotedColumns(profileColumns(profile), additions),
    [idParam]: surfaceKey,
  };
  const response = await client.submitForm(
    updateAction.path,
    updateAction.method,
    body,
    { Accept: "application/json+clicky" },
  );
  if (!response.success) {
    throw new Error(response.error ?? response.message ?? "Profile update failed");
  }
  return body;
}
