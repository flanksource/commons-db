import { useOperations, type OperationsApiClient } from "@flanksource/clicky-ui";
import type { ProfileColumn } from "@flanksource/clicky-ui/profiles";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { promoteProfileColumns } from "./profileColumnPromotion";
import { findProfileUpdateOperation } from "./profileUpdateOperation";
import { PROFILES_QUERY_KEY } from "./profilesQuery";

/** Saves JSON fields from a result row as profile columns and refreshes what depends on them. */
export function useColumnPromotion({
  client,
  surfaceKey,
  onSaved,
}: {
  client: OperationsApiClient;
  surfaceKey: string;
  onSaved: () => void;
}) {
  const queryClient = useQueryClient();
  const { operations, isLoading: operationsLoading } = useOperations(client);
  const updateAction = findProfileUpdateOperation(operations);
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState<string>();

  const save = async (additions: ProfileColumn[]) => {
    if (!updateAction) {
      setError("Profile update operations are unavailable");
      return;
    }
    setSaving(true);
    setError(undefined);
    try {
      await promoteProfileColumns({ client, updateAction, surfaceKey, additions });
      onSaved();
      setSuccess(`Added ${additions.length} ${additions.length === 1 ? "column" : "columns"}`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["openapi-spec"] }),
        queryClient.invalidateQueries({ queryKey: ["operation-list"] }),
        queryClient.invalidateQueries({ queryKey: ["profile-editor", surfaceKey] }),
        queryClient.invalidateQueries({ queryKey: ["profile-json-fields", surfaceKey] }),
        queryClient.invalidateQueries({ queryKey: ["logs-entity-names"] }),
        queryClient.invalidateQueries({ queryKey: PROFILES_QUERY_KEY }),
      ]);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Profile update failed");
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    setError(undefined);
    setSuccess(undefined);
  };

  return { operationsLoading, updateAction, error, saving, success, save, reset };
}

export type ColumnPromotion = ReturnType<typeof useColumnPromotion>;
