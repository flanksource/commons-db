import type {
  OperationsApiClient,
  ResolvedOperation,
} from "@flanksource/clicky-ui";
import type { ProfileColumn } from "./profileWizardModel";
import { mergePromotedColumns } from "./profileJsonFields";

type PromoteProfileColumnsOptions = {
  client: OperationsApiClient;
  getAction: ResolvedOperation;
  updateAction: ResolvedOperation;
  surfaceKey: string;
  additions: ProfileColumn[];
};

export async function loadProfileDocument(
  client: OperationsApiClient,
  getAction: ResolvedOperation,
  surfaceKey: string,
): Promise<Record<string, unknown>> {
  const response = await client.executeCommand(
    getAction.path,
    getAction.method,
    { id: surfaceKey },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error ?? response.message ?? `Unable to load profile ${surfaceKey}`);
  }
  if (!isRecord(response.parsed)) {
    throw new Error(`Profile ${surfaceKey} returned an invalid document`);
  }
  return response.parsed;
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
  getAction,
  updateAction,
  surfaceKey,
  additions,
}: PromoteProfileColumnsOptions): Promise<Record<string, unknown>> {
  if (!client.submitForm) {
    throw new Error("Profile updates are unavailable");
  }
  const profile = await loadProfileDocument(client, getAction, surfaceKey);
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
